package postgres

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"

	"github.com/MuunBob/hoodalpha/internal/config"
	"github.com/MuunBob/hoodalpha/migrations"
)

// Migrator applies versioned schema migrations. It opens its own database/sql
// handle because goose needs one; the application otherwise uses pgxpool.
type Migrator struct {
	db *sql.DB
}

// NewMigrator opens a migration connection. Call Close when done.
func NewMigrator(cfg config.PostgresConfig) (*Migrator, error) {
	db, err := sql.Open("pgx", cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("open migration connection: %w", err)
	}
	// Migrations are serial by nature; one connection avoids advisory-lock churn.
	db.SetMaxOpenConns(1)

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect(string(goose.DialectPostgres)); err != nil {
		db.Close()
		return nil, fmt.Errorf("set goose dialect: %w", err)
	}
	return &Migrator{db: db}, nil
}

// Close releases the migration connection.
func (m *Migrator) Close() error { return m.db.Close() }

// Up applies all pending migrations.
func (m *Migrator) Up(ctx context.Context) error {
	return goose.UpContext(ctx, m.db, migrations.Dir)
}

// Down rolls back exactly one migration.
func (m *Migrator) Down(ctx context.Context) error {
	return goose.DownContext(ctx, m.db, migrations.Dir)
}

// Reset rolls every migration back to version zero.
// Development and test only — it destroys all data, including financial records.
func (m *Migrator) Reset(ctx context.Context) error {
	return goose.DownToContext(ctx, m.db, migrations.Dir, 0)
}

// Status prints applied and pending migrations.
func (m *Migrator) Status(ctx context.Context) error {
	return goose.StatusContext(ctx, m.db, migrations.Dir)
}

// Version returns the currently applied schema version.
func (m *Migrator) Version(ctx context.Context) (int64, error) {
	return goose.GetDBVersionContext(ctx, m.db)
}

// Migrate is the one-shot helper used at application startup.
func Migrate(ctx context.Context, cfg config.PostgresConfig) error {
	m, err := NewMigrator(cfg)
	if err != nil {
		return err
	}
	defer m.Close()
	return m.Up(ctx)
}
