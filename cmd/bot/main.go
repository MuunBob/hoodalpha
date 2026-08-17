// Command bot runs the Telegram control plane.
//
// It is a separate process from api and worker so a Telegram outage, or a
// crash in command handling, cannot take down the health surface or stop
// background jobs.
package main

import (
	"context"
	"errors"
	"os"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/bootstrap"
	"github.com/MuunBob/hoodalpha/internal/config"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/observability/buildinfo"
	"github.com/MuunBob/hoodalpha/internal/observability/logging"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
	"github.com/MuunBob/hoodalpha/internal/persistence/redis"
	"github.com/MuunBob/hoodalpha/internal/queue"
	"github.com/MuunBob/hoodalpha/internal/telegram"
)

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

	if !cfg.Telegram.Enabled() {
		return errors.New("TELEGRAM_BOT_TOKEN is not set")
	}

	log.Info("starting telegram bot",
		"version", info.Version, "env", cfg.AppEnv,
		"chain", domain.ChainName(cfg.Chain.ChainID),
		// The number of allowed users is useful; the IDs are not logged.
		"allowlist_size", len(cfg.Telegram.AllowedUserIDs))

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

	users := postgres.NewUserRepo(deps.Postgres)
	wallets := postgres.NewWalletRepo(deps.Postgres)
	audit := postgres.NewAuditRepo(deps.Postgres)

	queueClient := queue.NewClient(cfg.Redis)
	defer func() { _ = queueClient.Close() }()

	allowlist := domain.NewAllowlist(toTelegramIDs(cfg.Telegram.AllowedUserIDs))
	onboarding := application.NewOnboarding(users, audit, allowlist, log)

	walletSvc := application.NewWalletService(application.WalletServiceDeps{
		Wallets:       wallets,
		Audit:         audit,
		Chain:         deps.Chain,
		Queue:         queue.NewEnqueuer(queueClient),
		ChainID:       cfg.Chain.ChainID,
		DefaultPolicy: defaultPolicy(cfg),
		Logger:        log,
	})

	health := application.NewHealthChecker(application.HealthCheckerDeps{
		Postgres:      deps.Postgres,
		Redis:         deps.Redis,
		Chain:         deps.Chain,
		ExpectedChain: cfg.Chain.ChainID,
		StaleAfter:    cfg.Chain.BlockStaleAfter,
		Version:       info.Version,
	})

	router := telegram.NewRouter(telegram.RouterOptions{
		Onboarding: onboarding,
		Limiter:    redis.NewRateLimiter(deps.Redis, "telegram"),
		Logger:     log,
		RateLimit:  cfg.Telegram.RateLimit,
		RateWindow: cfg.Telegram.RateWindow,
	})
	telegram.Register(router, telegram.CommandDeps{
		Health:     health,
		Wallets:    walletSvc,
		ChainID:    cfg.Chain.ChainID,
		MiniAppURL: cfg.Telegram.MiniAppURL,
		Version:    info.Version,
	})

	bot, err := telegram.NewBot(telegram.BotOptions{
		Token:  cfg.Telegram.BotToken,
		Router: router,
		Logger: log,
	})
	if err != nil {
		return err
	}

	log.Info("telegram control plane ready")
	if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func toTelegramIDs(ids []int64) []domain.TelegramUserID {
	out := make([]domain.TelegramUserID, 0, len(ids))
	for _, id := range ids {
		out = append(out, domain.TelegramUserID(id))
	}
	return out
}

// defaultPolicy seeds a newly linked wallet from configured risk settings, so
// limits exist from the moment a wallet appears. Trading is off regardless of
// configuration: enabling it is always a separate, audited decision.
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
