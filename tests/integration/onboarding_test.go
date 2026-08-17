package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
)

// setupSchema applies migrations and returns a connected pool.
func setupSchema(t *testing.T) *postgres.Pool {
	t.Helper()
	ctx := testContext(t, 2*time.Minute)
	cfg := postgresConfig(t)

	if err := postgres.Migrate(ctx, cfg); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	pool, err := postgres.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// uniqueTelegramID keeps parallel and repeated runs from colliding, without
// truncating tables another test may be using.
var telegramIDSeq int64 = 900000

func nextTelegramID(t *testing.T, pool *postgres.Pool) domain.TelegramUserID {
	t.Helper()
	telegramIDSeq++
	id := telegramIDSeq
	t.Cleanup(func() {
		ctx := context.Background()
		// wallets and policies cascade from the user; delete in FK order.
		_, _ = pool.Exec(ctx, `
			DELETE FROM wallets WHERE user_id IN
			    (SELECT user_id FROM telegram_users WHERE telegram_id = $1)`, id)
		_, _ = pool.Exec(ctx, `
			DELETE FROM users WHERE id IN
			    (SELECT user_id FROM telegram_users WHERE telegram_id = $1)`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM telegram_users WHERE telegram_id = $1`, id)
	})
	return domain.TelegramUserID(id)
}

func TestEnsureTelegramUserIsIdempotent(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	repo := postgres.NewUserRepo(pool)
	id := nextTelegramID(t, pool)

	tu := domain.TelegramUser{
		TelegramID: id,
		Username:   "tester",
		FirstName:  "Test",
		ChatID:     int64(id),
	}

	user, stored, created, err := repo.EnsureTelegramUser(ctx, tu)
	if err != nil {
		t.Fatalf("EnsureTelegramUser() error = %v", err)
	}
	if !created {
		t.Error("first contact did not report creation")
	}
	if user.ID == "" || stored.UserID != user.ID {
		t.Errorf("account link is inconsistent: user=%q telegram.user_id=%q", user.ID, stored.UserID)
	}

	// Called on every command, so a second call must reuse the account.
	again, _, created2, err := repo.EnsureTelegramUser(ctx, tu)
	if err != nil {
		t.Fatalf("second EnsureTelegramUser() error = %v", err)
	}
	if created2 {
		t.Error("second call created another account")
	}
	if again.ID != user.ID {
		t.Errorf("account changed between calls: %q then %q", user.ID, again.ID)
	}
}

// Usernames are mutable, so the cached copy must follow the user, while the
// account identity stays anchored to the numeric ID.
func TestEnsureTelegramUserRefreshesDisplayFields(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)
	repo := postgres.NewUserRepo(pool)
	id := nextTelegramID(t, pool)

	first, _, _, err := repo.EnsureTelegramUser(ctx, domain.TelegramUser{
		TelegramID: id, Username: "old_name", ChatID: int64(id),
	})
	if err != nil {
		t.Fatal(err)
	}

	second, stored, _, err := repo.EnsureTelegramUser(ctx, domain.TelegramUser{
		TelegramID: id, Username: "new_name", ChatID: int64(id),
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Username != "new_name" {
		t.Errorf("username = %q, want new_name", stored.Username)
	}
	if second.ID != first.ID {
		t.Error("a username change created a new account")
	}
}

func TestOnboardingRejectsUnauthorizedAndWritesNothing(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)

	users := postgres.NewUserRepo(pool)
	audit := postgres.NewAuditRepo(pool)
	allowed := nextTelegramID(t, pool)
	stranger := nextTelegramID(t, pool)

	onboarding := application.NewOnboarding(users, audit,
		domain.NewAllowlist([]domain.TelegramUserID{allowed}), nil)

	_, err := onboarding.Authorize(ctx, domain.TelegramUser{
		TelegramID: stranger, ChatID: int64(stranger),
	})
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}

	// The rejection must not have created an account.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM telegram_users WHERE telegram_id = $1`, int64(stranger)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("unauthorized user created %d rows; want 0", count)
	}

	// And it must be in the audit trail — a burst of these is what an attack
	// looks like.
	events, err := audit.ListByAction(ctx, domain.ActionTelegramUnauthorized, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.ActorID == "telegram:"+itoa(int64(stranger)) {
			found = true
			if e.Outcome != domain.OutcomeRejected {
				t.Errorf("outcome = %q, want rejected", e.Outcome)
			}
		}
	}
	if !found {
		t.Error("rejection was not written to the audit trail")
	}
}

func TestOnboardingAuthorizesAllowlistedUser(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)

	id := nextTelegramID(t, pool)
	onboarding := application.NewOnboarding(
		postgres.NewUserRepo(pool), postgres.NewAuditRepo(pool),
		domain.NewAllowlist([]domain.TelegramUserID{id}), nil)

	identity, err := onboarding.Authorize(ctx, domain.TelegramUser{
		TelegramID: id, Username: "operator", ChatID: int64(id),
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if !identity.FirstContact {
		t.Error("first contact was not reported")
	}
	if identity.User.Status != domain.UserActive {
		t.Errorf("status = %q, want active", identity.User.Status)
	}
}

// An operator must be able to revoke access without editing configuration and
// restarting the process.
func TestSuspendedUserIsRefused(t *testing.T) {
	pool := setupSchema(t)
	ctx := testContext(t, time.Minute)

	id := nextTelegramID(t, pool)
	onboarding := application.NewOnboarding(
		postgres.NewUserRepo(pool), postgres.NewAuditRepo(pool),
		domain.NewAllowlist([]domain.TelegramUserID{id}), nil)

	identity, err := onboarding.Authorize(ctx, domain.TelegramUser{TelegramID: id, ChatID: int64(id)})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE users SET status = 'suspended' WHERE id = $1`, identity.User.ID.String()); err != nil {
		t.Fatal(err)
	}

	if _, err := onboarding.Authorize(ctx, domain.TelegramUser{TelegramID: id, ChatID: int64(id)}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("suspended account was authorized: %v", err)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
