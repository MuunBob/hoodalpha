// Command api serves the operational HTTP surface (health, readiness, version)
// and maintains the WebSocket chain-head subscription.
package main

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sync/errgroup"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/bootstrap"
	"github.com/MuunBob/hoodalpha/internal/chain"
	"github.com/MuunBob/hoodalpha/internal/config"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/httpapi"
	"github.com/MuunBob/hoodalpha/internal/observability/buildinfo"
	"github.com/MuunBob/hoodalpha/internal/observability/logging"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
	redisstore "github.com/MuunBob/hoodalpha/internal/persistence/redis"
	"github.com/MuunBob/hoodalpha/internal/queue"
	"github.com/MuunBob/hoodalpha/internal/telegram/initdata"
)

func toTelegramIDs(ids []int64) []domain.TelegramUserID {
	out := make([]domain.TelegramUserID, 0, len(ids))
	for _, id := range ids {
		out = append(out, domain.TelegramUserID(id))
	}
	return out
}

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
		// The logger may not exist yet if config failed, so use stderr directly.
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
	log.Info("starting api",
		"version", info.Version, "commit", info.Commit,
		"env", cfg.AppEnv, "chain", domain.ChainName(cfg.Chain.ChainID))

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

	// The head subscriber is optional: without RH_WS_URL the bot still reads
	// the chain over HTTP, it just loses push-based head updates.
	var head *chain.HeadSubscriber
	if cfg.Chain.WSURL != "" {
		head = chain.NewHeadSubscriber(chain.SubscriberOptions{
			WSURL:         cfg.Chain.WSURL,
			ExpectedChain: cfg.Chain.ChainID,
			DialTimeout:   cfg.Chain.RequestTimeout,
			Logger:        log,
		})
	} else {
		log.Warn("RH_WS_URL not set; running without websocket head subscription")
	}

	queueClient := queue.NewClient(cfg.Redis)
	defer func() { _ = queueClient.Close() }()

	syncRepo := postgres.NewSyncStateRepo(deps.Postgres)
	chainSync := application.NewChainSync(deps.Chain, syncRepo, cfg.Chain.ChainID, log)

	// Report how far behind we are before doing anything else. Later phases
	// gate trading on this reconciliation step.
	if last, ok, err := chainSync.LastSeen(ctx); err != nil {
		log.Error("could not read last synced head", "error", err)
	} else if ok {
		log.Info("resuming from persisted head", "block", last.Number)
	} else {
		log.Info("no persisted head; starting fresh")
	}

	healthDeps := application.HealthCheckerDeps{
		Postgres:      deps.Postgres,
		Redis:         deps.Redis,
		Chain:         deps.Chain,
		ExpectedChain: cfg.Chain.ChainID,
		StaleAfter:    cfg.Chain.BlockStaleAfter,
		Version:       info.Version,
	}
	if head != nil {
		healthDeps.Head = head
	}
	health := application.NewHealthChecker(healthDeps)

	// The Mini App backend is only mounted when a bot token is configured.
	// Without one, initData cannot be verified, so exposing the routes would
	// mean exposing endpoints that could never authenticate anyone.
	miniApp := httpapi.MiniAppDeps{ChainID: cfg.Chain.ChainID}
	if cfg.Telegram.Enabled() {
		verifier, err := initdata.NewVerifier(initdata.Options{
			BotToken: cfg.Telegram.BotToken,
			TTL:      cfg.Telegram.InitDataTTL,
			// Replay protection lives in Redis so a replayed payload is
			// refused even when it reaches a different replica.
			Guard: redisstore.NewReplayGuard(deps.Redis, "miniapp"),
		})
		if err != nil {
			return err
		}
		onboarding := application.NewOnboarding(
			postgres.NewUserRepo(deps.Postgres),
			postgres.NewAuditRepo(deps.Postgres),
			domain.NewAllowlist(toTelegramIDs(cfg.Telegram.AllowedUserIDs)),
			log)

		miniApp.Auth = application.NewMiniAppAuth(verifier, onboarding,
			postgres.NewAuditRepo(deps.Postgres), log)
		miniApp.Wallets = application.NewWalletService(application.WalletServiceDeps{
			Wallets:       postgres.NewWalletRepo(deps.Postgres),
			Audit:         postgres.NewAuditRepo(deps.Postgres),
			Chain:         deps.Chain,
			Queue:         queue.NewEnqueuer(queueClient),
			ChainID:       cfg.Chain.ChainID,
			DefaultPolicy: defaultPolicy(cfg),
			Logger:        log,
		})
		log.Info("mini app backend enabled", "init_data_ttl", cfg.Telegram.InitDataTTL.String())
	} else {
		log.Warn("TELEGRAM_BOT_TOKEN not set; mini app backend disabled")
	}

	srv := httpapi.New(httpapi.Options{
		Addr:    cfg.HTTPAddr,
		Health:  health,
		Logger:  log,
		MiniApp: miniApp,
	})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return srv.Run(gctx, cfg.ShutdownTimeout) })

	if head != nil {
		g.Go(func() error {
			err := head.Run(gctx, func(ref domain.BlockRef) {
				log.Debug("new head", "block", ref.Number)
				if err := chainSync.Record(gctx, ref); err != nil {
					log.Error("persist head failed", "block", ref.Number, "error", err)
				}
			})
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		})
	}

	log.Info("api ready")
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
