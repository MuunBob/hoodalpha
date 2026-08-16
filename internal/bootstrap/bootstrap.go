// Package bootstrap wires the application's dependencies. Every binary builds
// its dependencies here so wiring lives in one place instead of drifting
// between cmd/ entrypoints.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/MuunBob/hoodalpha/internal/chain"
	"github.com/MuunBob/hoodalpha/internal/config"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
	"github.com/MuunBob/hoodalpha/internal/persistence/redis"
)

// Deps are the shared infrastructure handles. Close releases all of them.
type Deps struct {
	Cfg      config.Config
	Log      *slog.Logger
	Postgres *postgres.Pool
	Redis    *redis.Client
	Chain    *chain.Client

	closers []func() error
}

// Option toggles which dependencies get built. The worker needs everything;
// a migration command needs only Postgres.
type Option func(*options)

type options struct {
	postgres bool
	redis    bool
	chain    bool
	migrate  bool
}

// WithPostgres connects the database pool.
func WithPostgres() Option { return func(o *options) { o.postgres = true } }

// WithRedis connects the Redis client.
func WithRedis() Option { return func(o *options) { o.redis = true } }

// WithChain dials the RPC endpoint and verifies the chain ID.
func WithChain() Option { return func(o *options) { o.chain = true } }

// WithMigrations applies pending migrations before returning. Implies Postgres.
func WithMigrations() Option {
	return func(o *options) { o.migrate = true; o.postgres = true }
}

// New builds the requested dependencies. On any failure it closes whatever it
// already opened, so a partial startup never leaks connections.
func New(ctx context.Context, cfg config.Config, log *slog.Logger, opts ...Option) (*Deps, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	d := &Deps{Cfg: cfg, Log: log}

	if o.migrate {
		log.Info("applying database migrations")
		if err := postgres.Migrate(ctx, cfg.Postgres); err != nil {
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}

	if o.postgres {
		pool, err := postgres.Connect(ctx, cfg.Postgres)
		if err != nil {
			d.closeAll()
			return nil, err
		}
		d.Postgres = pool
		d.closers = append(d.closers, func() error { pool.Close(); return nil })
		log.Info("postgres connected")
	}

	if o.redis {
		rdb, err := redis.Connect(ctx, cfg.Redis)
		if err != nil {
			d.closeAll()
			return nil, err
		}
		d.Redis = rdb
		d.closers = append(d.closers, rdb.Close)
		log.Info("redis connected", "addr", cfg.Redis.Addr)
	}

	if o.chain {
		c, err := chain.Dial(ctx, chain.Options{
			RPCURL:         cfg.Chain.RPCURL,
			ExpectedChain:  cfg.Chain.ChainID,
			RequestTimeout: cfg.Chain.RequestTimeout,
			MaxRetries:     cfg.Chain.MaxRetries,
			RetryBackoff:   cfg.Chain.RetryBackoff,
		})
		if err != nil {
			d.closeAll()
			return nil, err
		}
		d.Chain = c
		d.closers = append(d.closers, func() error { c.Close(); return nil })
		log.Info("chain rpc connected", "chain_id", cfg.Chain.ChainID)
	}

	return d, nil
}

// Close releases every dependency in reverse order of construction.
func (d *Deps) Close() error { return d.closeAll() }

func (d *Deps) closeAll() error {
	var errs []error
	for i := len(d.closers) - 1; i >= 0; i-- {
		if err := d.closers[i](); err != nil {
			errs = append(errs, err)
		}
	}
	d.closers = nil
	return errors.Join(errs...)
}

// SignalContext returns a context cancelled on SIGINT or SIGTERM, so every
// binary shuts down through the same path.
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}
