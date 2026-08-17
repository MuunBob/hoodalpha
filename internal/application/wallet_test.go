package application_test

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
)

const linkAddress = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"

// fakeWalletStore is an in-memory WalletStore.
type fakeWalletStore struct {
	mu       sync.Mutex
	wallets  map[string]domain.Wallet
	policies map[string]domain.WalletPolicy
	seq      int
}

func newFakeWalletStore() *fakeWalletStore {
	return &fakeWalletStore{
		wallets:  map[string]domain.Wallet{},
		policies: map[string]domain.WalletPolicy{},
	}
}

func (f *fakeWalletStore) Link(_ context.Context, w domain.Wallet, p domain.WalletPolicy) (domain.Wallet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.wallets {
		if existing.UserID == w.UserID && existing.Address == w.Address && existing.ChainID == w.ChainID {
			return domain.Wallet{}, domain.ErrWalletAlreadyLinked
		}
	}
	f.seq++
	w.ID = "wallet-" + string(rune('a'+f.seq-1))
	w.Status = domain.WalletPending
	w.CreatedAt = time.Now().UTC()
	f.wallets[w.ID] = w
	p.WalletID = w.ID
	f.policies[w.ID] = p
	return w, nil
}

func (f *fakeWalletStore) Get(_ context.Context, id string) (domain.Wallet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.wallets[id]
	if !ok {
		return domain.Wallet{}, errors.New("not found")
	}
	return w, nil
}

func (f *fakeWalletStore) ListByUser(_ context.Context, userID domain.UserID) ([]domain.Wallet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Wallet
	for _, w := range f.wallets {
		if w.UserID == userID {
			out = append(out, w)
		}
	}
	return out, nil
}

func (f *fakeWalletStore) MarkVerified(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.wallets[id]
	if !ok {
		return errors.New("not found")
	}
	// Mirrors the SQL guard: only a pending wallet is promoted.
	if w.Status != domain.WalletPending {
		return nil
	}
	w.Status = domain.WalletActive
	w.VerifiedAt = &at
	f.wallets[id] = w
	return nil
}

func (f *fakeWalletStore) SetStatus(_ context.Context, id string, next domain.WalletStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.wallets[id]
	if !ok {
		return errors.New("not found")
	}
	if !w.Status.CanTransitionTo(next) {
		return errors.New("illegal transition")
	}
	w.Status = next
	f.wallets[id] = w
	return nil
}

func (f *fakeWalletStore) GetPolicy(_ context.Context, walletID string) (domain.WalletPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.policies[walletID]
	if !ok {
		return domain.WalletPolicy{}, errors.New("not found")
	}
	return p, nil
}

func (f *fakeWalletStore) UpdatePolicy(_ context.Context, walletID string, p domain.WalletPolicy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.policies[walletID]; !ok {
		return errors.New("not found")
	}
	p.WalletID = walletID
	f.policies[walletID] = p
	return nil
}

// stubProbe answers chain reads.
type stubProbe struct {
	id      uint64
	balance *big.Int
	err     error
}

func (s stubProbe) ChainID(context.Context) (uint64, error) { return s.id, s.err }
func (s stubProbe) BalanceAt(context.Context, domain.Address, *uint64) (domain.Wei, error) {
	if s.err != nil {
		return domain.Wei{}, s.err
	}
	return domain.NewWei(s.balance), nil
}

// recordingQueue captures enqueued work.
type recordingQueue struct {
	mu       sync.Mutex
	verified []string
	notified int
}

func (q *recordingQueue) EnqueueWalletVerify(_ context.Context, walletID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.verified = append(q.verified, walletID)
	return nil
}

func (q *recordingQueue) EnqueueNotification(context.Context, int64, string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.notified++
	return nil
}

type auditRecorder struct {
	mu     sync.Mutex
	events []domain.AuditEvent
}

func (a *auditRecorder) Record(_ context.Context, e domain.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

func (a *auditRecorder) findOutcome(action, outcome string) (domain.AuditEvent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.events {
		if e.Action == action && e.Outcome == outcome {
			return e, true
		}
	}
	return domain.AuditEvent{}, false
}

func (a *auditRecorder) find(action string) (domain.AuditEvent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.events {
		if e.Action == action {
			return e, true
		}
	}
	return domain.AuditEvent{}, false
}

func defaultTestPolicy() domain.WalletPolicy {
	return domain.WalletPolicy{
		MaxPositionPercent:     5,
		MaxOpenPositions:       5,
		DailyLossLimitPercent:  10,
		StopLossPercent:        5,
		CapitalRecoveryPercent: 100,
		MaxSlippageBPS:         100,
		MinLiquidityUSD:        10000,
		// Deliberately true here to prove the service forces it off.
		TradingEnabled: true,
	}
}

func newWalletService(store *fakeWalletStore, probe application.AddressProbe, audit *auditRecorder, queue *recordingQueue) *application.WalletService {
	return application.NewWalletService(application.WalletServiceDeps{
		Wallets:       store,
		Audit:         audit,
		Chain:         probe,
		Queue:         queue,
		ChainID:       4663,
		DefaultPolicy: defaultTestPolicy(),
	})
}

func TestLinkCreatesPendingWalletAndEnqueuesVerification(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663, balance: big.NewInt(0)}, audit, q)

	w, err := svc.Link(context.Background(), application.LinkRequest{
		UserID:  "user-1",
		Address: linkAddress,
		Role:    domain.WalletRoleOwner,
		ActorID: "telegram:1001",
	})
	if err != nil {
		t.Fatalf("Link() error = %v", err)
	}

	// A wallet must not be usable before the chain has confirmed it.
	if w.Status != domain.WalletPending {
		t.Errorf("status = %q, want pending", w.Status)
	}
	if len(q.verified) != 1 || q.verified[0] != w.ID {
		t.Errorf("verification not enqueued: %v", q.verified)
	}
	if _, ok := audit.find(domain.ActionWalletLinked); !ok {
		t.Error("link was not audited")
	}
}

// Linking must never enable trading, even if the configured default says so.
func TestLinkNeverEnablesTrading(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663}, audit, q)

	w, err := svc.Link(context.Background(), application.LinkRequest{
		UserID: "user-1", Address: linkAddress, Role: domain.WalletRoleOwner,
	})
	if err != nil {
		t.Fatalf("Link() error = %v", err)
	}

	p, err := store.GetPolicy(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.TradingEnabled {
		t.Error("linking a wallet enabled trading; it must always be a separate decision")
	}
}

func TestLinkRejectsInvalidAddress(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663}, audit, q)

	for _, addr := range []string{"nope", "0xdeadbeef", "", "0x0000000000000000000000000000000000000000"} {
		if _, err := svc.Link(context.Background(), application.LinkRequest{
			UserID: "user-1", Address: addr, Role: domain.WalletRoleOwner,
		}); err == nil {
			t.Errorf("Link() accepted %q", addr)
		}
	}
	if len(q.verified) != 0 {
		t.Error("verification was enqueued for a rejected address")
	}
}

func TestLinkRejectsDuplicate(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663}, audit, q)

	req := application.LinkRequest{UserID: "user-1", Address: linkAddress, Role: domain.WalletRoleOwner}
	if _, err := svc.Link(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Link(context.Background(), req); !errors.Is(err, domain.ErrWalletAlreadyLinked) {
		t.Errorf("error = %v, want ErrWalletAlreadyLinked", err)
	}
}

func TestVerifyActivatesWallet(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663, balance: big.NewInt(1000)}, audit, q)

	w, err := svc.Link(context.Background(), application.LinkRequest{
		UserID: "user-1", Address: linkAddress, Role: domain.WalletRoleOwner,
	})
	if err != nil {
		t.Fatal(err)
	}

	verified, err := svc.Verify(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Status != domain.WalletActive {
		t.Errorf("status = %q, want active", verified.Status)
	}
	if verified.VerifiedAt == nil {
		t.Error("verified_at was not set")
	}
	if _, ok := audit.find(domain.ActionWalletVerified); !ok {
		t.Error("verification was not audited")
	}
}

// An unfunded wallet is a normal starting state, not an invalid one.
func TestVerifyAcceptsZeroBalance(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663, balance: big.NewInt(0)}, audit, q)

	w, _ := svc.Link(context.Background(), application.LinkRequest{
		UserID: "user-1", Address: linkAddress, Role: domain.WalletRoleOwner,
	})
	verified, err := svc.Verify(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("Verify() rejected an unfunded wallet: %v", err)
	}
	if verified.Status != domain.WalletActive {
		t.Errorf("status = %q, want active", verified.Status)
	}
}

// Verifying against the wrong network proves nothing, so it must fail.
func TestVerifyRejectsWrongChain(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663}, audit, q)

	w, _ := svc.Link(context.Background(), application.LinkRequest{
		UserID: "user-1", Address: linkAddress, Role: domain.WalletRoleOwner,
	})

	// The node now reports a different chain.
	wrong := newWalletService(store, stubProbe{id: 1}, audit, q)
	if _, err := wrong.Verify(context.Background(), w.ID); !errors.Is(err, domain.ErrWrongChain) {
		t.Fatalf("error = %v, want ErrWrongChain", err)
	}

	stored, _ := store.Get(context.Background(), w.ID)
	if stored.Status == domain.WalletActive {
		t.Error("wallet was activated despite a chain mismatch")
	}
	if _, ok := audit.find(domain.ActionWalletVerifyFailed); !ok {
		t.Error("failed verification was not audited")
	}
}

// Task delivery is at-least-once, so verification must be safe to repeat.
func TestVerifyIsIdempotent(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663, balance: big.NewInt(5)}, audit, q)

	w, _ := svc.Link(context.Background(), application.LinkRequest{
		UserID: "user-1", Address: linkAddress, Role: domain.WalletRoleOwner,
	})

	first, err := svc.Verify(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Verify(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("second Verify() error = %v", err)
	}
	if !first.VerifiedAt.Equal(*second.VerifiedAt) {
		t.Error("repeat verification changed verified_at")
	}
}

// A replayed verification task must not resurrect a wallet an operator
// deliberately disabled.
func TestVerifyDoesNotResurrectDisabledWallet(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663, balance: big.NewInt(5)}, audit, q)

	w, _ := svc.Link(context.Background(), application.LinkRequest{
		UserID: "user-1", Address: linkAddress, Role: domain.WalletRoleOwner,
	})
	if err := store.SetStatus(context.Background(), w.ID, domain.WalletDisabled); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Verify(context.Background(), w.ID); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	stored, _ := store.Get(context.Background(), w.ID)
	if stored.Status != domain.WalletDisabled {
		t.Errorf("status = %q; a disabled wallet was reactivated by a replayed task", stored.Status)
	}
}

// A client can send any wallet ID; the server scopes the query, not the client.
func TestGetEnforcesOwnership(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663}, audit, q)

	w, _ := svc.Link(context.Background(), application.LinkRequest{
		UserID: "user-1", Address: linkAddress, Role: domain.WalletRoleOwner,
	})

	if _, err := svc.Get(context.Background(), "user-2", w.ID); !errors.Is(err, domain.ErrWalletNotOwned) {
		t.Errorf("error = %v, want ErrWalletNotOwned", err)
	}
	if _, err := svc.Get(context.Background(), "user-1", w.ID); err != nil {
		t.Errorf("owner was refused their own wallet: %v", err)
	}
}

func TestUpdatePolicyEnforcesOwnershipAndValidates(t *testing.T) {
	store, audit, q := newFakeWalletStore(), &auditRecorder{}, &recordingQueue{}
	svc := newWalletService(store, stubProbe{id: 4663}, audit, q)

	w, _ := svc.Link(context.Background(), application.LinkRequest{
		UserID: "user-1", Address: linkAddress, Role: domain.WalletRoleOwner,
	})

	valid := domain.WalletPolicy{
		MaxPositionPercent: 10, MaxOpenPositions: 3, DailyLossLimitPercent: 5,
		StopLossPercent: 7, CapitalRecoveryPercent: 100, MaxSlippageBPS: 50,
		MinLiquidityUSD: 5000, TradingEnabled: true,
	}

	if _, err := svc.UpdatePolicy(context.Background(), "user-2", w.ID, valid, "actor"); !errors.Is(err, domain.ErrWalletNotOwned) {
		t.Errorf("a non-owner changed a policy: %v", err)
	}

	unsafe := valid
	unsafe.MaxPositionPercent = 500
	if _, err := svc.UpdatePolicy(context.Background(), "user-1", w.ID, unsafe, "actor"); err == nil {
		t.Error("an unsafe policy was accepted")
	}

	updated, err := svc.UpdatePolicy(context.Background(), "user-1", w.ID, valid, "actor")
	if err != nil {
		t.Fatalf("UpdatePolicy() error = %v", err)
	}
	if updated.MaxPositionPercent != 10 || !updated.TradingEnabled {
		t.Errorf("policy not applied: %+v", updated)
	}

	// The rejected attempt is audited too, so look for the successful one.
	e, ok := audit.findOutcome(domain.ActionPolicyChanged, domain.OutcomeOK)
	if !ok {
		t.Fatal("successful policy change was not audited")
	}
	// The audit must say what changed, not merely that something did:
	// during an incident the question is always which limit moved.
	if e.Detail["before"] == nil || e.Detail["after"] == nil {
		t.Error("audit did not record before/after values")
	}

	// The rejected attempt must also leave a trail.
	if _, ok := audit.findOutcome(domain.ActionPolicyChanged, domain.OutcomeRejected); !ok {
		t.Error("rejected policy change was not audited")
	}
}
