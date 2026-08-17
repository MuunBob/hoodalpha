package telegram_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/telegram"
)

// fakeUserStore is an in-memory UserStore.
type fakeUserStore struct {
	mu      sync.Mutex
	users   map[domain.TelegramUserID]domain.User
	status  domain.UserStatus
	calls   int
	failErr error
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{users: map[domain.TelegramUserID]domain.User{}, status: domain.UserActive}
}

func (f *fakeUserStore) EnsureTelegramUser(_ context.Context, tu domain.TelegramUser) (domain.User, domain.TelegramUser, bool, error) {
	if f.failErr != nil {
		return domain.User{}, domain.TelegramUser{}, false, f.failErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++

	u, ok := f.users[tu.TelegramID]
	created := !ok
	if created {
		u = domain.User{
			ID:     domain.UserID("user-" + itoa(int64(tu.TelegramID))),
			Status: f.status,
		}
		f.users[tu.TelegramID] = u
	}
	return u, tu, created, nil
}

func (f *fakeUserStore) GetTelegramUser(context.Context, domain.TelegramUserID) (domain.TelegramUser, error) {
	return domain.TelegramUser{}, errors.New("not used")
}

func (f *fakeUserStore) GetUser(context.Context, domain.UserID) (domain.User, error) {
	return domain.User{}, errors.New("not used")
}

// writeCount reports how many accounts were created, so a test can assert an
// unauthorized user never reached the database.
func (f *fakeUserStore) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeAudit records events in memory.
type fakeAudit struct {
	mu     sync.Mutex
	events []domain.AuditEvent
}

func (f *fakeAudit) Record(_ context.Context, e domain.AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func (f *fakeAudit) has(action string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e.Action == action {
			return true
		}
	}
	return false
}

func (f *fakeAudit) count(action string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if e.Action == action {
			n++
		}
	}
	return n
}

// recorder captures replies.
type recorder struct {
	mu       sync.Mutex
	messages []string
}

func (r *recorder) Send(_ context.Context, _ int64, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, text)
	return nil
}

func (r *recorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.messages) == 0 {
		return ""
	}
	return r.messages[len(r.messages)-1]
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

// countingLimiter allows a fixed number of calls, then throttles.
type countingLimiter struct {
	mu    sync.Mutex
	calls int
	max   int
	err   error
}

func (l *countingLimiter) Allow(context.Context, string, int, time.Duration) (bool, int, error) {
	if l.err != nil {
		return true, 0, l.err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	return l.calls <= l.max, l.max - l.calls, nil
}

const allowedID = domain.TelegramUserID(1001)

func newTestRouter(t *testing.T, store *fakeUserStore, audit *fakeAudit, limiter telegram.RateLimiter) *telegram.Router {
	t.Helper()
	onboarding := application.NewOnboarding(store, audit,
		domain.NewAllowlist([]domain.TelegramUserID{allowedID}), nil)

	r := telegram.NewRouter(telegram.RouterOptions{
		Onboarding: onboarding,
		Limiter:    limiter,
	})
	r.Handle(telegram.Command{
		Name:        "ping",
		Description: "test command",
		Handler: func(context.Context, telegram.Request) (string, error) {
			return "pong", nil
		},
	})
	return r
}

func update(id domain.TelegramUserID, text string) telegram.Update {
	return telegram.Update{TelegramID: id, ChatID: int64(id), Text: text, Username: "tester"}
}

func TestAuthorizedUserReachesHandler(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	r := newTestRouter(t, store, audit, nil)

	r.Dispatch(context.Background(), update(allowedID, "/ping"), rec)

	if rec.last() != "pong" {
		t.Errorf("reply = %q, want pong", rec.last())
	}
	if !audit.has(domain.ActionUserOnboarded) {
		t.Error("first contact was not audited as onboarding")
	}
	if !audit.has(domain.ActionTelegramCommand) {
		t.Error("command invocation was not audited")
	}
}

// The core control-plane property: an identity outside the allowlist must not
// reach a handler, and must not create any state by trying.
func TestUnauthorizedUserIsRejected(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	r := newTestRouter(t, store, audit, nil)

	r.Dispatch(context.Background(), update(domain.TelegramUserID(6666), "/ping"), rec)

	if got := rec.last(); got != "Not authorized." {
		t.Errorf("reply = %q, want a terse refusal", got)
	}
	if store.writeCount() != 0 {
		t.Errorf("unauthorized user caused %d database writes; want 0", store.writeCount())
	}
	if !audit.has(domain.ActionTelegramUnauthorized) {
		t.Error("rejection was not audited")
	}
}

// The refusal must not reveal whether the command exists, who is allowed, or
// why access was denied.
func TestRejectionLeaksNothing(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	r := newTestRouter(t, store, audit, nil)

	r.Dispatch(context.Background(), update(domain.TelegramUserID(6666), "/ping"), rec)

	reply := strings.ToLower(rec.last())
	for _, leak := range []string{"allowlist", "allowed", "1001", "ping", "unknown command"} {
		if strings.Contains(reply, leak) {
			t.Errorf("refusal leaked %q: %s", leak, rec.last())
		}
	}
}

// An account can be suspended without editing configuration and restarting.
func TestSuspendedAccountIsRejected(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	store.status = domain.UserSuspended
	r := newTestRouter(t, store, audit, nil)

	r.Dispatch(context.Background(), update(allowedID, "/ping"), rec)

	if rec.last() != "Not authorized." {
		t.Errorf("suspended account reply = %q", rec.last())
	}
}

func TestUnknownCommandIsAudited(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	r := newTestRouter(t, store, audit, nil)

	r.Dispatch(context.Background(), update(allowedID, "/nonexistent"), rec)

	if !strings.Contains(rec.last(), "Unknown command") {
		t.Errorf("reply = %q", rec.last())
	}
	if audit.count(domain.ActionTelegramCommand) != 1 {
		t.Error("unknown command was not audited")
	}
}

func TestNonCommandTextIsIgnored(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	r := newTestRouter(t, store, audit, nil)

	r.Dispatch(context.Background(), update(allowedID, "hello there"), rec)

	if rec.count() != 0 {
		t.Errorf("bot replied to non-command chatter: %q", rec.last())
	}
}

// Telegram appends @BotName in group chats; the router must still route.
func TestCommandWithBotSuffixRoutes(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	r := newTestRouter(t, store, audit, nil)

	r.Dispatch(context.Background(), update(allowedID, "/ping@HoodAlphaBot extra"), rec)

	if rec.last() != "pong" {
		t.Errorf("reply = %q, want pong", rec.last())
	}
}

func TestCommandArgumentsArePassed(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	onboarding := application.NewOnboarding(store, audit,
		domain.NewAllowlist([]domain.TelegramUserID{allowedID}), nil)

	r := telegram.NewRouter(telegram.RouterOptions{Onboarding: onboarding})
	r.Handle(telegram.Command{
		Name: "echo",
		Handler: func(_ context.Context, req telegram.Request) (string, error) {
			return strings.Join(req.Args, "|"), nil
		},
	})

	r.Dispatch(context.Background(), update(allowedID, "/echo one two"), rec)
	if rec.last() != "one|two" {
		t.Errorf("args = %q, want one|two", rec.last())
	}
}

func TestRateLimitBlocksAfterBudget(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	limiter := &countingLimiter{max: 2}
	r := newTestRouter(t, store, audit, limiter)

	for i := 0; i < 3; i++ {
		r.Dispatch(context.Background(), update(allowedID, "/ping"), rec)
	}

	if !strings.Contains(rec.last(), "Slow down") {
		t.Errorf("third command was not throttled: %q", rec.last())
	}
	if !audit.has(domain.ActionTelegramRateLimited) {
		t.Error("throttling was not audited")
	}
}

// A Redis outage must not lock the operator out of their own control plane.
// The allowlist still gates access, so failing open here is bounded.
func TestRateLimiterFailureAllowsCommand(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	limiter := &countingLimiter{max: 0, err: errors.New("redis is down")}
	r := newTestRouter(t, store, audit, limiter)

	r.Dispatch(context.Background(), update(allowedID, "/ping"), rec)

	if rec.last() != "pong" {
		t.Errorf("reply = %q; a limiter outage must not block an authorized user", rec.last())
	}
}

// A handler error must not leak internals, which can carry connection strings
// or internal paths.
func TestHandlerErrorIsNotLeaked(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	onboarding := application.NewOnboarding(store, audit,
		domain.NewAllowlist([]domain.TelegramUserID{allowedID}), nil)

	r := telegram.NewRouter(telegram.RouterOptions{Onboarding: onboarding})
	r.Handle(telegram.Command{
		Name: "boom",
		Handler: func(context.Context, telegram.Request) (string, error) {
			return "", errors.New("postgres://user:hunter2@db:5432 connection refused")
		},
	})

	r.Dispatch(context.Background(), update(allowedID, "/boom"), rec)

	if strings.Contains(rec.last(), "hunter2") || strings.Contains(rec.last(), "postgres") {
		t.Errorf("handler error leaked to the user: %q", rec.last())
	}
	if !audit.has(domain.ActionTelegramCommand) {
		t.Error("failed command was not audited")
	}
}

func TestBotAccountsAreIgnored(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	r := newTestRouter(t, store, audit, nil)

	u := update(allowedID, "/ping")
	u.IsBot = true
	r.Dispatch(context.Background(), u, rec)

	if rec.count() != 0 {
		t.Errorf("replied to a bot account: %q", rec.last())
	}
}

func TestDuplicateCommandRegistrationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a duplicate command did not panic")
		}
	}()
	r := telegram.NewRouter(telegram.RouterOptions{
		Onboarding: application.NewOnboarding(newFakeUserStore(), &fakeAudit{},
			domain.NewAllowlist(nil), nil),
	})
	noop := func(context.Context, telegram.Request) (string, error) { return "", nil }
	r.Handle(telegram.Command{Name: "dup", Handler: noop})
	r.Handle(telegram.Command{Name: "dup", Handler: noop})
}

// An empty allowlist must admit nobody. An unconfigured deployment being open
// to the world is the worst possible default for a bot that will hold funds.
func TestEmptyAllowlistAdmitsNobody(t *testing.T) {
	store, audit, rec := newFakeUserStore(), &fakeAudit{}, &recorder{}
	onboarding := application.NewOnboarding(store, audit, domain.NewAllowlist(nil), nil)

	r := telegram.NewRouter(telegram.RouterOptions{Onboarding: onboarding})
	r.Handle(telegram.Command{
		Name:    "ping",
		Handler: func(context.Context, telegram.Request) (string, error) { return "pong", nil },
	})

	r.Dispatch(context.Background(), update(allowedID, "/ping"), rec)
	if rec.last() != "Not authorized." {
		t.Errorf("empty allowlist admitted a user: %q", rec.last())
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
