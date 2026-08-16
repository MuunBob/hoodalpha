package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
)

func TestPostgresConnectAndHealth(t *testing.T) {
	ctx := testContext(t, 30*time.Second)
	pool, err := postgres.Connect(ctx, postgresConfig(t))
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer pool.Close()

	if err := pool.Health(ctx); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
}

// TestMigrationsUpDownUp proves the schema can be built, torn down and rebuilt.
// A migration that only works forwards is a migration you cannot roll back
// during an incident.
func TestMigrationsUpDownUp(t *testing.T) {
	ctx := testContext(t, 3*time.Minute)
	cfg := postgresConfig(t)

	m, err := postgres.NewMigrator(cfg)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	defer m.Close()

	// Start from zero so the test does not depend on prior state.
	if err := m.Reset(ctx); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if v, err := m.Version(ctx); err != nil || v != 0 {
		t.Fatalf("after Reset: version = %d, err = %v; want 0", v, err)
	}

	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	topVersion, err := m.Version(ctx)
	if err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if topVersion == 0 {
		t.Fatal("Up() left version at 0")
	}

	// Roll every migration back, then forward again to the same version.
	if err := m.Reset(ctx); err != nil {
		t.Fatalf("second Reset() error = %v", err)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatalf("second Up() error = %v", err)
	}
	v, err := m.Version(ctx)
	if err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if v != topVersion {
		t.Errorf("version after rebuild = %d, want %d", v, topVersion)
	}
}

// TestPartmanConfigured verifies the extension is installed, audit_logs is a
// partitioned table, pg_partman knows about it, and rows route to a child
// partition rather than piling into the default.
func TestPartmanConfigured(t *testing.T) {
	ctx := testContext(t, 2*time.Minute)
	cfg := postgresConfig(t)

	if err := postgres.Migrate(ctx, cfg); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	pool, err := postgres.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer pool.Close()

	var extVersion string
	err = pool.QueryRow(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'pg_partman'`).Scan(&extVersion)
	if err != nil {
		t.Fatalf("pg_partman extension not installed: %v", err)
	}
	t.Logf("pg_partman version %s", extVersion)

	// The parent must be declaratively partitioned ('p' = partitioned table).
	var relkind string
	err = pool.QueryRow(ctx, `
		SELECT c.relkind
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'timeseries' AND c.relname = 'audit_logs'`).Scan(&relkind)
	if err != nil {
		t.Fatalf("timeseries.audit_logs not found: %v", err)
	}
	if relkind != "p" {
		t.Errorf("audit_logs relkind = %q, want \"p\" (partitioned table)", relkind)
	}

	// pg_partman must be managing it, with the retention policy we configured.
	var (
		control   string
		interval  string
		retention *string
		keepTable bool
		autoMaint string
	)
	err = pool.QueryRow(ctx, `
		SELECT control, partition_interval, retention, retention_keep_table, automatic_maintenance
		  FROM partman.part_config
		 WHERE parent_table = 'timeseries.audit_logs'`).
		Scan(&control, &interval, &retention, &keepTable, &autoMaint)
	if err != nil {
		t.Fatalf("audit_logs missing from partman.part_config: %v", err)
	}
	if control != "occurred_at" {
		t.Errorf("control column = %q, want occurred_at", control)
	}
	if retention == nil || *retention == "" {
		t.Error("retention not configured; audit partitions would grow forever")
	}
	if keepTable {
		t.Error("retention_keep_table = true; dropped partitions would linger as detached tables")
	}
	if autoMaint != "on" {
		t.Errorf("automatic_maintenance = %q, want on", autoMaint)
	}

	// Children must already exist, otherwise every insert lands in default.
	var childCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM pg_inherits i
		  JOIN pg_class c ON c.oid = i.inhrelid
		  JOIN pg_class p ON p.oid = i.inhparent
		  JOIN pg_namespace n ON n.oid = p.relnamespace
		 WHERE n.nspname = 'timeseries' AND p.relname = 'audit_logs'`).Scan(&childCount)
	if err != nil {
		t.Fatalf("count children: %v", err)
	}
	if childCount < 2 {
		t.Errorf("audit_logs has %d children, want at least 2 (premake should build ahead)", childCount)
	}
	t.Logf("audit_logs has %d child partitions", childCount)
}

// TestAuditLogPartitionRouting inserts a row and asserts it landed in a real
// time-range child, not the catch-all default partition.
func TestAuditLogPartitionRouting(t *testing.T) {
	ctx := testContext(t, 2*time.Minute)
	cfg := postgresConfig(t)

	if err := postgres.Migrate(ctx, cfg); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	pool, err := postgres.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer pool.Close()

	occurredAt := time.Now().UTC()
	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO timeseries.audit_logs (occurred_at, actor_type, action, detail)
		VALUES ($1, 'system', 'test:partition_routing', '{"test":true}'::jsonb)
		RETURNING id`, occurredAt).Scan(&id)
	if err != nil {
		t.Fatalf("insert audit log: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM timeseries.audit_logs WHERE action = 'test:partition_routing'`)
	})

	var partition string
	err = pool.QueryRow(ctx, `
		SELECT tableoid::regclass::text
		  FROM timeseries.audit_logs
		 WHERE id = $1 AND occurred_at = $2`, id, occurredAt).Scan(&partition)
	if err != nil {
		t.Fatalf("locate partition: %v", err)
	}
	t.Logf("row routed to %s", partition)

	if strings.HasSuffix(partition, "_default") {
		t.Errorf("row landed in the default partition (%s); premake is not keeping up", partition)
	}
	// pg_partman 5.x names monthly children parent_pYYYYMMDD, using the start
	// date of the range rather than a YYYY_MM suffix.
	want := "timeseries.audit_logs_p" + occurredAt.Format("200601") + "01"
	if partition != want {
		t.Errorf("partition = %s, want %s", partition, want)
	}
}

// TestPartmanMaintenanceRuns exercises the maintenance procedure the background
// worker calls on a schedule. If this errors, partitions silently stop being
// created and inserts start falling into the default weeks later.
func TestPartmanMaintenanceRuns(t *testing.T) {
	ctx := testContext(t, 2*time.Minute)
	cfg := postgresConfig(t)

	if err := postgres.Migrate(ctx, cfg); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	pool, err := postgres.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `CALL partman.run_maintenance_proc()`); err != nil {
		t.Fatalf("run_maintenance_proc() error = %v", err)
	}

	// check_default reports rows stuck in the catch-all partition.
	var defaultRows int64
	err = pool.QueryRow(ctx,
		`SELECT coalesce(sum(count), 0) FROM partman.check_default()`).Scan(&defaultRows)
	if err != nil {
		t.Fatalf("check_default() error = %v", err)
	}
	if defaultRows != 0 {
		t.Errorf("%d rows sitting in default partitions; partition coverage has a gap", defaultRows)
	}
}

func TestSyncStateRepoIsMonotonic(t *testing.T) {
	ctx := testContext(t, time.Minute)
	cfg := postgresConfig(t)

	if err := postgres.Migrate(ctx, cfg); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	pool, err := postgres.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer pool.Close()

	repo := postgres.NewSyncStateRepo(pool)
	const chainID = 999999 // reserved for tests; never a real network
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM chain_sync_state WHERE chain_id = $1`, chainID)
	})

	if _, ok, err := repo.LoadHead(ctx, chainID); err != nil || ok {
		t.Fatalf("LoadHead on empty state: ok = %v, err = %v; want false, nil", ok, err)
	}

	high := domain.BlockRef{
		Number: 100,
		Hash:   "0x1111111111111111111111111111111111111111111111111111111111111111",
		Time:   time.Now().UTC().Truncate(time.Second),
	}
	if err := repo.SaveHead(ctx, chainID, "test-chain", high); err != nil {
		t.Fatalf("SaveHead() error = %v", err)
	}

	// A replayed or out-of-order observation must not rewind progress. Asynq
	// delivers at-least-once, so this case happens in normal operation.
	low := domain.BlockRef{
		Number: 50,
		Hash:   "0x2222222222222222222222222222222222222222222222222222222222222222",
		Time:   high.Time.Add(-time.Minute),
	}
	if err := repo.SaveHead(ctx, chainID, "test-chain", low); err != nil {
		t.Fatalf("SaveHead(older) error = %v", err)
	}

	got, ok, err := repo.LoadHead(ctx, chainID)
	if err != nil || !ok {
		t.Fatalf("LoadHead() ok = %v, err = %v", ok, err)
	}
	if got.Number != 100 {
		t.Errorf("head rewound to %d; want 100 (writes must be monotonic)", got.Number)
	}
	if got.Hash != high.Hash {
		t.Errorf("hash = %q, want %q", got.Hash, high.Hash)
	}

	// A newer observation must advance it.
	next := domain.BlockRef{
		Number: 101,
		Hash:   "0x3333333333333333333333333333333333333333333333333333333333333333",
		Time:   high.Time.Add(time.Second),
	}
	if err := repo.SaveHead(ctx, chainID, "test-chain", next); err != nil {
		t.Fatalf("SaveHead(newer) error = %v", err)
	}
	got, _, err = repo.LoadHead(ctx, chainID)
	if err != nil {
		t.Fatalf("LoadHead() error = %v", err)
	}
	if got.Number != 101 {
		t.Errorf("head = %d, want 101", got.Number)
	}
}

// TestSyncStateRejectsMalformedHash proves the database constraint, not just
// the Go validation, refuses a non-canonical hash.
func TestSyncStateRejectsMalformedHash(t *testing.T) {
	ctx := testContext(t, time.Minute)
	cfg := postgresConfig(t)

	if err := postgres.Migrate(ctx, cfg); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	pool, err := postgres.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, `
		INSERT INTO chain_sync_state
		    (chain_id, chain_name, last_block_number, last_block_hash, last_block_time)
		VALUES (999998, 'bad', 1, '0xNOTAHASH', now())`)
	if err == nil {
		_, _ = pool.Exec(ctx, `DELETE FROM chain_sync_state WHERE chain_id = 999998`)
		t.Fatal("database accepted a malformed block hash")
	}
}
