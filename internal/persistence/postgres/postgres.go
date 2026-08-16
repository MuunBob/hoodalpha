// Package postgres owns the connection pool to the source-of-truth database.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MuunBob/hoodalpha/internal/config"
)

// Pool wraps pgxpool with the project's defaults.
type Pool struct {
	*pgxpool.Pool
}

// Connect builds a pool and verifies it with a ping.
func Connect(ctx context.Context, cfg config.PostgresConfig) (*Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse postgres url: %w", err)
	}
	pc.MaxConns = cfg.MaxConns
	pc.MinConns = cfg.MinConns
	pc.MaxConnLifetime = cfg.MaxConnLifetime
	pc.MaxConnIdleTime = cfg.MaxConnIdleTime

	// Always store and compare timestamps in UTC. Partition boundaries and
	// daily PnL windows become ambiguous otherwise.
	if pc.ConnConfig.RuntimeParams == nil {
		pc.ConnConfig.RuntimeParams = map[string]string{}
	}
	pc.ConnConfig.RuntimeParams["timezone"] = "UTC"

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Pool{Pool: pool}, nil
}

// Health runs a cheap round-trip query.
func (p *Pool) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var one int
	return p.QueryRow(ctx, "SELECT 1").Scan(&one)
}

// Close releases all connections.
func (p *Pool) Close() {
	if p.Pool != nil {
		p.Pool.Close()
	}
}
