package integration

import (
	"context"
	"errors"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
	redisstore "github.com/MuunBob/hoodalpha/internal/persistence/redis"
	"github.com/MuunBob/hoodalpha/internal/queue"
	"github.com/MuunBob/hoodalpha/internal/queue/tasks"
	"github.com/MuunBob/hoodalpha/internal/telegram/initdata"
)

// fixedProbe answers chain reads without a live node, so this test exercises
// the queue and persistence path rather than the RPC provider.
type fixedProbe struct {
	chainID uint64
	balance *big.Int
}

func (p fixedProbe) ChainID(context.Context) (uint64, error) { return p.chainID, nil }
func (p fixedProbe) BalanceAt(context.Context, domain.Address, *uint64) (domain.Wei, error) {
	return domain.NewWei(p.balance), nil
}

// TestWalletVerifyTaskEndToEnd runs the real path: link a wallet, let the
// enqueued task be picked up by a real Asynq worker, and assert the wallet is
// activated in PostgreSQL and the event is in the audit trail.
func TestWalletVerifyTaskEndToEnd(t *testing.T) {
	pool := setupSchema(t)
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	ctx := testContext(t, 2*time.Minute)
	userID := newTestUser(t, pool)

	queueClient := queue.NewClient(redisCfg)
	t.Cleanup(func() { _ = queueClient.Close() })

	walletRepo := postgres.NewWalletRepo(pool)
	auditRepo := postgres.NewAuditRepo(pool)

	svc := application.NewWalletService(application.WalletServiceDeps{
		Wallets:       walletRepo,
		Audit:         auditRepo,
		Chain:         fixedProbe{chainID: 4663, balance: big.NewInt(1_000_000)},
		Queue:         queue.NewEnqueuer(queueClient),
		ChainID:       4663,
		DefaultPolicy: testPolicy(),
		Logger:        slog.Default(),
	})

	srv := queue.NewServer(redisCfg, testWorkerConfig(), slog.Default())
	srv.Handle(queue.TypeWalletVerify, tasks.WalletVerify(svc, slog.Default()))
	runServer(t, srv)

	wallet, err := svc.Link(ctx, application.LinkRequest{
		UserID:  userID,
		Address: "0x9999999999999999999999999999999999999999",
		Role:    domain.WalletRoleOwner,
		ActorID: "telegram:test",
	})
	if err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	if wallet.Status != domain.WalletPending {
		t.Fatalf("status = %q, want pending immediately after linking", wallet.Status)
	}

	// Wait for the worker to promote it.
	deadline := time.Now().Add(45 * time.Second)
	var final domain.Wallet
	for time.Now().Before(deadline) {
		final, err = walletRepo.Get(ctx, wallet.ID)
		if err != nil {
			t.Fatal(err)
		}
		if final.Status == domain.WalletActive {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if final.Status != domain.WalletActive {
		t.Fatalf("wallet still %q after 45s; the verify task did not run", final.Status)
	}
	if final.VerifiedAt == nil {
		t.Error("verified_at was not set")
	}

	events, err := auditRepo.ListByAction(ctx, domain.ActionWalletVerified, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.SubjectID == wallet.ID {
			found = true
		}
	}
	if !found {
		t.Error("verification was not written to the audit trail")
	}
}

// A payload that can never succeed must be archived rather than retried
// forever, otherwise one bad message occupies a worker slot indefinitely.
func TestWalletVerifyArchivesUnknownWallet(t *testing.T) {
	pool := setupSchema(t)
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	ctx := testContext(t, time.Minute)

	svc := application.NewWalletService(application.WalletServiceDeps{
		Wallets:       postgres.NewWalletRepo(pool),
		Audit:         postgres.NewAuditRepo(pool),
		Chain:         fixedProbe{chainID: 4663, balance: big.NewInt(0)},
		ChainID:       4663,
		DefaultPolicy: testPolicy(),
		Logger:        slog.Default(),
	})

	handler := tasks.WalletVerify(svc, slog.Default())
	err := handler(ctx, []byte(`{"wallet_id":"00000000-0000-0000-0000-000000000000"}`))
	if err == nil {
		t.Fatal("verifying a nonexistent wallet succeeded")
	}
	if !queue.IsSkipRetry(err) {
		t.Errorf("error = %v; want SkipRetry so a doomed task is archived, not looped", err)
	}
}

// A malformed payload will never parse, so retrying it wastes worker slots.
func TestWalletVerifyArchivesMalformedPayload(t *testing.T) {
	svc := application.NewWalletService(application.WalletServiceDeps{
		ChainID: 4663, Logger: slog.Default(),
	})
	handler := tasks.WalletVerify(svc, slog.Default())

	for _, payload := range []string{`not json`, `{}`, `{"wallet_id":""}`} {
		err := handler(context.Background(), []byte(payload))
		if err == nil {
			t.Errorf("payload %q was accepted", payload)
			continue
		}
		if !queue.IsSkipRetry(err) {
			t.Errorf("payload %q: error = %v, want SkipRetry", payload, err)
		}
	}
}

// The enqueuer derives a task ID from the wallet ID, so a double-submit
// schedules one verification rather than two.
func TestWalletVerifyEnqueueIsDeduplicated(t *testing.T) {
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)
	ctx := testContext(t, 30*time.Second)

	client := queue.NewClient(redisCfg)
	t.Cleanup(func() { _ = client.Close() })
	enq := queue.NewEnqueuer(client)

	// No worker is running, so the task stays pending and the ID stays taken.
	if err := enq.EnqueueWalletVerify(ctx, "wallet-dedup-1"); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// The duplicate is reported as success: the work is already scheduled.
	if err := enq.EnqueueWalletVerify(ctx, "wallet-dedup-1"); err != nil {
		t.Fatalf("duplicate enqueue returned an error: %v", err)
	}
}

// The replay guard must be shared across processes, so this exercises the real
// Redis implementation rather than an in-memory stand-in.
func TestRedisReplayGuardBlocksSecondUse(t *testing.T) {
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	ctx := testContext(t, 30*time.Second)
	client, err := redisstore.Connect(ctx, redisCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	guard := redisstore.NewReplayGuard(client, "test-replay")

	first, err := guard.FirstUse("hash-abc", time.Minute)
	if err != nil || !first {
		t.Fatalf("FirstUse() = (%v, %v); want (true, nil)", first, err)
	}
	second, err := guard.FirstUse("hash-abc", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Error("the same hash was accepted twice; replay protection is not working")
	}

	// A different payload is unaffected.
	other, err := guard.FirstUse("hash-xyz", time.Minute)
	if err != nil || !other {
		t.Errorf("an unrelated hash was refused: (%v, %v)", other, err)
	}
}

// Full Mini App path against a real Redis guard: a captured payload must be
// refused on its second use even though its signature is still valid.
func TestMiniAppReplayRejectedWithRedisGuard(t *testing.T) {
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	ctx := testContext(t, 30*time.Second)
	client, err := redisstore.Connect(ctx, redisCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	const token = "123456:INTEGRATION-TEST-TOKEN"
	verifier, err := initdata.NewVerifier(initdata.Options{
		BotToken: token,
		TTL:      15 * time.Minute,
		Guard:    redisstore.NewReplayGuard(client, "test-miniapp"),
	})
	if err != nil {
		t.Fatal(err)
	}

	raw := signTestInitData(t, token, time.Now().UTC())

	if _, err := verifier.Verify(raw); err != nil {
		t.Fatalf("first Verify() error = %v", err)
	}
	if _, err := verifier.Verify(raw); !errors.Is(err, initdata.ErrReplayed) {
		t.Fatalf("second Verify() error = %v, want ErrReplayed", err)
	}
}
