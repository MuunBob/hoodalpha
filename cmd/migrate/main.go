// Command migrate applies, rolls back and inspects database migrations.
//
// Usage:
//
//	migrate up       apply all pending migrations
//	migrate down     roll back exactly one migration
//	migrate reset    roll back to zero (destroys all data; blocked in production)
//	migrate status   list applied and pending migrations
//	migrate version  print the current schema version
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MuunBob/hoodalpha/internal/bootstrap"
	"github.com/MuunBob/hoodalpha/internal/config"
	"github.com/MuunBob/hoodalpha/internal/observability/logging"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
)

func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString("fatal: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: migrate <up|down|reset|status|version>")
	}
	cmd := os.Args[1]

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(cfg.LogLevel, cfg.LogJSON)

	ctx, stop := bootstrap.SignalContext(context.Background())
	defer stop()

	m, err := postgres.NewMigrator(cfg.Postgres)
	if err != nil {
		return err
	}
	defer m.Close()

	switch cmd {
	case "up":
		if err := m.Up(ctx); err != nil {
			return err
		}
		v, err := m.Version(ctx)
		if err != nil {
			return err
		}
		log.Info("migrations applied", "version", v)

	case "down":
		if err := m.Down(ctx); err != nil {
			return err
		}
		v, err := m.Version(ctx)
		if err != nil {
			return err
		}
		log.Info("migration rolled back", "version", v)

	case "reset":
		// Dropping every table destroys financial history. Refuse in production
		// regardless of what the operator typed.
		if cfg.IsProduction() {
			return errors.New("refusing to reset a production database")
		}
		if err := m.Reset(ctx); err != nil {
			return err
		}
		log.Warn("database reset to version 0")

	case "status":
		return m.Status(ctx)

	case "version":
		v, err := m.Version(ctx)
		if err != nil {
			return err
		}
		fmt.Println(v)

	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}
