// Command worker runs the Asynq background job server and the periodic
// scheduler that feeds it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hibiken/asynq"
	"golang.org/x/sync/errgroup"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/bootstrap"
	"github.com/MuunBob/hoodalpha/internal/config"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/observability/buildinfo"
	"github.com/MuunBob/hoodalpha/internal/observability/logging"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
	"github.com/MuunBob/hoodalpha/internal/queue"
	"github.com/MuunBob/hoodalpha/internal/queue/tasks"
	"github.com/MuunBob/hoodalpha/internal/telegram"
)

// defaultPolicy seeds a newly linked wallet from configured risk settings.
// Trading is off regardless of configuration.
func defaultPolicy(cfg config.Config) domain.WalletPolicy {
	return domain.WalletPolicy{
		MaxPositionPercent:     cfg.Risk.MaxPositionPercent,
		MaxOpenPositions:       cfg.Risk.MaxOpenPositions,
		DailyLossLimitPercent:  cfg.Risk.DailyLossLimitPercent,
		StopLossPercent:        cfg.Risk.StopLossPercent,
		CapitalRecoveryPercent: cfg.Risk.CapitalRecoveryPercent,
		MaxSlippageBPS:         cfg.Risk.MaxSlippageBPS,
		MinLiquidityUSD:        cfg.Risk.MinLiquidityUSD,
		TradingEnabled:         false,
	}
}

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		os.Stderr.WriteString("fatal: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg.LogLevel, cfg.LogJSON)
	info := buildinfo.Get()
	log.Info("starting worker",
		"version", info.Version, "env", cfg.AppEnv,
		"chain", domain.ChainName(cfg.Chain.ChainID),
		"concurrency", cfg.Worker.Concurrency)

	ctx, stop := bootstrap.SignalContext(context.Background())
	defer stop()

	deps, err := bootstrap.New(ctx, cfg, log,
		bootstrap.WithPostgres(), bootstrap.WithRedis(), bootstrap.WithChain())
	if err != nil {
		return err
	}
	defer func() {
		if err := deps.Close(); err != nil {
			log.Error("shutdown error", "error", err)
		}
		log.Info("shutdown complete")
	}()

	syncRepo := postgres.NewSyncStateRepo(deps.Postgres)
	chainSync := application.NewChainSync(deps.Chain, syncRepo, cfg.Chain.ChainID, log)
	health := application.NewHealthChecker(application.HealthCheckerDeps{
		Postgres:      deps.Postgres,
		Redis:         deps.Redis,
		Chain:         deps.Chain,
		ExpectedChain: cfg.Chain.ChainID,
		StaleAfter:    cfg.Chain.BlockStaleAfter,
		Version:       info.Version,
	})

	queueClient := queue.NewClient(cfg.Redis)
	defer func() { _ = queueClient.Close() }()

	walletSvc := application.NewWalletService(application.WalletServiceDeps{
		Wallets:       postgres.NewWalletRepo(deps.Postgres),
		Audit:         postgres.NewAuditRepo(deps.Postgres),
		Chain:         deps.Chain,
		Queue:         queue.NewEnqueuer(queueClient),
		ChainID:       cfg.Chain.ChainID,
		DefaultPolicy: defaultPolicy(cfg),
		Logger:        log,
	})

	srv := queue.NewServer(cfg.Redis, cfg.Worker, log)
	srv.Handle(queue.TypeSystemHealthCheck, tasks.HealthCheck(health, log))
	srv.Handle(queue.TypeChainSyncHead, tasks.SyncHead(chainSync, log))
	srv.Handle(queue.TypeWalletVerify, tasks.WalletVerify(walletSvc, log))

	// Notifications need the Telegram transport. Without a token the handler
	// is not registered at all, so a queued message fails visibly rather than
	// being silently dropped by a no-op handler.
	if cfg.Telegram.Enabled() {
		notifier, err := telegram.NewNotifier(cfg.Telegram.BotToken, log)
		if err != nil {
			return err
		}
		srv.Handle(queue.TypeTelegramNotification, tasks.TelegramNotification(notifier, log))
		log.Info("telegram notifications enabled")
	} else {
		log.Warn("TELEGRAM_BOT_TOKEN not set; telegram notifications disabled")
	}

	scheduler := newScheduler(cfg, log)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return srv.Run(gctx) })
	g.Go(func() error { return runScheduler(gctx, scheduler, log) })

	log.Info("worker ready", "queues", cfg.Worker.Queues)
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// newScheduler registers the periodic tasks. Asynq's scheduler stores its state
// in Redis, so two worker replicas will not double-schedule the same entry.
func newScheduler(cfg config.Config, log *slog.Logger) *asynq.Scheduler {
	s := asynq.NewScheduler(queue.RedisOpt(cfg.Redis), &asynq.SchedulerOpts{
		Location: time.UTC,
		Logger:   schedulerLogger{log.With("component", "scheduler")},
	})

	// Health probe every minute on the maintenance queue.
	if _, err := s.Register("* * * * *",
		asynq.NewTask(queue.TypeSystemHealthCheck, nil),
		asynq.Queue(queue.QueueMaintenance),
		asynq.MaxRetry(1),
		asynq.Timeout(30*time.Second),
		asynq.Retention(time.Hour),
	); err != nil {
		log.Error("register health check schedule", "error", err)
	}

	// Persist the chain head every minute so a restart has a recent anchor
	// even when the WebSocket subscription was unavailable.
	if _, err := s.Register("* * * * *",
		asynq.NewTask(queue.TypeChainSyncHead, nil),
		asynq.Queue(queue.QueueMaintenance),
		asynq.MaxRetry(3),
		asynq.Timeout(30*time.Second),
		asynq.Retention(time.Hour),
	); err != nil {
		log.Error("register chain sync schedule", "error", err)
	}

	return s
}

func runScheduler(ctx context.Context, s *asynq.Scheduler, log *slog.Logger) error {
	if err := s.Start(); err != nil {
		return err
	}
	<-ctx.Done()
	log.Info("stopping scheduler")
	s.Shutdown()
	return nil
}

type schedulerLogger struct{ l *slog.Logger }

func (s schedulerLogger) Debug(args ...any) { s.l.Debug(fmt.Sprint(args...)) }
func (s schedulerLogger) Info(args ...any)  { s.l.Info(fmt.Sprint(args...)) }
func (s schedulerLogger) Warn(args ...any)  { s.l.Warn(fmt.Sprint(args...)) }
func (s schedulerLogger) Error(args ...any) { s.l.Error(fmt.Sprint(args...)) }
func (s schedulerLogger) Fatal(args ...any) { s.l.Error(fmt.Sprint(args...)) }
