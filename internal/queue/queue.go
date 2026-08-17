// Package queue wraps Asynq. It defines the queue names, the task type
// vocabulary, and the enqueue/serve plumbing shared by every background job.
package queue

import (
	"github.com/hibiken/asynq"

	"github.com/MuunBob/hoodalpha/internal/config"
)

// Queue names, ordered by how urgently work on them must run.
const (
	QueueCritical      = "critical"
	QueueDefault       = "default"
	QueueAnalysis      = "analysis"
	QueueMarket        = "market"
	QueueNotifications = "notifications"
	QueueMaintenance   = "maintenance"
)

// Task types. Only the health-check task is implemented in this phase; the
// remaining names are reserved so later phases do not rename queues in flight.
const (
	// Implemented now.
	TypeSystemHealthCheck    = "system:health_check"
	TypeChainSyncHead        = "chain:sync_head"
	TypeWalletVerify         = "wallet:verify"
	TypeTelegramNotification = "telegram:notification"

	// Reserved for later phases.
	TypeTokenDiscover        = "token:discover"
	TypeTokenAnalyze         = "token:analyze"
	TypeTokenSecurityScan    = "token:security_scan"
	TypeTokenMarketScan      = "token:market_scan"
	TypeTokenInsiderScan     = "token:insider_scan"
	TypeTokenSocialScan      = "token:social_scan"
	TypeSignalScore          = "signal:score"
	TypeSignalNotify         = "signal:notify"
	TypeTradeSimulate        = "trade:simulate"
	TypeTradeSubmit          = "trade:submit"
	TypeTradeMonitor         = "trade:monitor"
	TypePositionUpdate       = "position:update"
	TypePositionRecoverCap   = "position:recover_capital"
	TypePositionUpdateRunner = "position:update_runner"
	TypePnLUpdate            = "pnl:update"
	TypeSystemReconcile      = "system:reconcile"
)

// RedisOpt builds the Asynq connection options from application config.
func RedisOpt(cfg config.RedisConfig) asynq.RedisClientOpt {
	return asynq.RedisClientOpt{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
	}
}
