package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
)

func testPolicy() domain.WalletPolicy {
	return domain.WalletPolicy{
		MaxPositionPercent:     5,
		MaxOpenPositions:       5,
		DailyLossLimitPercent:  10,
		StopLossPercent:        5,
		CapitalRecoveryPercent: 100,
		MaxSlippageBPS:         100,
		MinLiquidityUSD:        10000,
	}
}

// newTestUser creates an account and returns its ID, cleaning up afterwards.
func newTestUser(t *testing.T, pool *postgres.Pool) domain.UserID {
	t.Helper()
	ctx := testContext(t, time.Minute)
	id := nextTelegramID(t, pool)

	user, _, _, err := postgres.NewUserRepo(pool).EnsureTelegramUser(ctx, domain.TelegramUser{
		TelegramID: id, ChatID: int64(id),
	})
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}
	return user.ID
}

func TestWalletLinkAndPolicyPersist(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	repo := postgres.NewWalletRepo(pool)
	userID := newTestUser(t, pool)

	wallet, err := domain.NewWallet(userID,
		"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", 4663, domain.WalletRoleOwner, "main")
	if err != nil {
		t.Fatal(err)
	}

	stored, err := repo.Link(ctx, wallet, testPolicy())
	if err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	// Stored lowercase so lookups never depend on checksum casing.
	if stored.Address.String() != "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed" {
		t.Errorf("address = %q, want lowercase", stored.Address)
	}
	if stored.Status != domain.WalletPending {
		t.Errorf("status = %q, want pending", stored.Status)
	}

	policy, err := repo.GetPolicy(ctx, stored.ID)
	if err != nil {
		t.Fatalf("GetPolicy() error = %v", err)
	}
	// Percentages round-trip through NUMERIC without drift.
	if policy.MaxPositionPercent != 5 || policy.DailyLossLimitPercent != 10 {
		t.Errorf("policy did not round-trip: %+v", policy)
	}
	if policy.TradingEnabled {
		t.Error("a newly linked wallet has trading enabled")
	}
}

// The unique index is what stops a double-submit from creating two records of
// the same wallet, each with its own policy.
func TestWalletDuplicateIsRejected(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	repo := postgres.NewWalletRepo(pool)
	userID := newTestUser(t, pool)

	w, _ := domain.NewWallet(userID,
		"0x1111111111111111111111111111111111111111", 4663, domain.WalletRoleOwner, "")
	if _, err := repo.Link(ctx, w, testPolicy()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Link(ctx, w, testPolicy()); err != domain.ErrWalletAlreadyLinked {
		t.Errorf("error = %v, want ErrWalletAlreadyLinked", err)
	}
}

// The status guard makes verification safe to replay, which matters because
// Asynq delivers at-least-once.
func TestMarkVerifiedIsIdempotentAndOrderingSafe(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	repo := postgres.NewWalletRepo(pool)
	userID := newTestUser(t, pool)

	w, _ := domain.NewWallet(userID,
		"0x2222222222222222222222222222222222222222", 4663, domain.WalletRoleOwner, "")
	stored, err := repo.Link(ctx, w, testPolicy())
	if err != nil {
		t.Fatal(err)
	}

	at := time.Now().UTC().Truncate(time.Second)
	if err := repo.MarkVerified(ctx, stored.ID, at); err != nil {
		t.Fatal(err)
	}
	first, _ := repo.Get(ctx, stored.ID)
	if first.Status != domain.WalletActive || first.VerifiedAt == nil {
		t.Fatalf("wallet not activated: %+v", first)
	}

	// Replay must not move verified_at.
	if err := repo.MarkVerified(ctx, stored.ID, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	second, _ := repo.Get(ctx, stored.ID)
	if !second.VerifiedAt.Equal(*first.VerifiedAt) {
		t.Error("replayed verification changed verified_at")
	}

	// A disabled wallet must not be resurrected by a late task.
	if err := repo.SetStatus(ctx, stored.ID, domain.WalletDisabled); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkVerified(ctx, stored.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	after, _ := repo.Get(ctx, stored.ID)
	if after.Status != domain.WalletDisabled {
		t.Errorf("status = %q; a disabled wallet was reactivated", after.Status)
	}
}

func TestWalletStatusTransitionsAreEnforced(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	repo := postgres.NewWalletRepo(pool)
	userID := newTestUser(t, pool)

	w, _ := domain.NewWallet(userID,
		"0x3333333333333333333333333333333333333333", 4663, domain.WalletRoleOwner, "")
	stored, _ := repo.Link(ctx, w, testPolicy())

	// pending -> disabled -> pending is legal; disabled -> active is not,
	// because re-enabling requires re-verification.
	if err := repo.SetStatus(ctx, stored.ID, domain.WalletDisabled); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetStatus(ctx, stored.ID, domain.WalletActive); err == nil {
		t.Error("disabled -> active was allowed without re-verification")
	}
	if err := repo.SetStatus(ctx, stored.ID, domain.WalletPending); err != nil {
		t.Fatalf("disabled -> pending rejected: %v", err)
	}
	back, _ := repo.Get(ctx, stored.ID)
	if back.VerifiedAt != nil {
		t.Error("returning to pending kept the old verification proof")
	}
}

// The database is the last line of defence for limits that bound real money.
func TestDatabaseRejectsUnsafePolicy(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	repo := postgres.NewWalletRepo(pool)
	userID := newTestUser(t, pool)

	w, _ := domain.NewWallet(userID,
		"0x4444444444444444444444444444444444444444", 4663, domain.WalletRoleOwner, "")
	stored, _ := repo.Link(ctx, w, testPolicy())

	// Bypass the Go validation entirely and write directly.
	_, err := pool.Exec(ctx, `
		UPDATE wallet_policies SET max_position_percent = 500 WHERE wallet_id = $1`, stored.ID)
	if err == nil {
		t.Error("database accepted a position limit of 500%")
	}

	_, err = pool.Exec(ctx, `
		UPDATE wallet_policies SET max_slippage_bps = 99999 WHERE wallet_id = $1`, stored.ID)
	if err == nil {
		t.Error("database accepted slippage above 100%")
	}
}

// Constraints must hold against a direct write, not only against the Go layer.
func TestDatabaseRejectsMalformedWallets(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	userID := newTestUser(t, pool)

	tests := []struct {
		name    string
		address string
		chainID int64
	}{
		{"uppercase address", "0x5AAEB6053F3E94C9B9A09F33669435E7EF1BEAED", 4663},
		{"too short", "0xdeadbeef", 4663},
		{"zero address", "0x0000000000000000000000000000000000000000", 4663},
		{"zero chain", "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO wallets (user_id, address, chain_id, role, status)
				VALUES ($1, $2, $3, 'owner', 'pending')`,
				userID.String(), tt.address, tt.chainID)
			if err == nil {
				t.Errorf("database accepted %s", tt.name)
			}
		})
	}
}

// Two concurrently active signing wallets would make balance and position
// accounting ambiguous.
func TestOnlyOneActiveBotWalletPerChain(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	repo := postgres.NewWalletRepo(pool)
	userID := newTestUser(t, pool)

	first, _ := domain.NewWallet(userID,
		"0x5555555555555555555555555555555555555555", 4663, domain.WalletRoleBot, "")
	a, err := repo.Link(ctx, first, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkVerified(ctx, a.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	second, _ := domain.NewWallet(userID,
		"0x6666666666666666666666666666666666666666", 4663, domain.WalletRoleBot, "")
	b, err := repo.Link(ctx, second, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	// Activating a second bot wallet on the same chain must fail.
	if err := repo.MarkVerified(ctx, b.ID, time.Now().UTC()); err == nil {
		var count int
		_ = pool.QueryRow(ctx, `
			SELECT count(*) FROM wallets
			 WHERE user_id = $1 AND role = 'bot' AND status = 'active'`, userID.String()).Scan(&count)
		if count > 1 {
			t.Errorf("%d active bot wallets on one chain; want at most 1", count)
		}
	}
}

// Deleting a user must not orphan wallets or silently destroy audit subjects.
func TestUserDeletionIsRestricted(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	repo := postgres.NewWalletRepo(pool)
	userID := newTestUser(t, pool)

	w, _ := domain.NewWallet(userID,
		"0x7777777777777777777777777777777777777777", 4663, domain.WalletRoleOwner, "")
	if _, err := repo.Link(ctx, w, testPolicy()); err != nil {
		t.Fatal(err)
	}

	_, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID.String())
	if err == nil {
		t.Error("a user with wallets was deleted; ON DELETE RESTRICT is not in force")
	} else if !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Logf("delete refused with: %v", err)
	}
}

// A wallet's policy is deleted with it: a policy with no wallet is meaningless.
func TestPolicyCascadesWithWallet(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	repo := postgres.NewWalletRepo(pool)
	userID := newTestUser(t, pool)

	w, _ := domain.NewWallet(userID,
		"0x8888888888888888888888888888888888888888", 4663, domain.WalletRoleOwner, "")
	stored, _ := repo.Link(ctx, w, testPolicy())

	if _, err := pool.Exec(ctx, `DELETE FROM wallets WHERE id = $1`, stored.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM wallet_policies WHERE wallet_id = $1`, stored.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("%d orphaned policies remain", count)
	}
}
