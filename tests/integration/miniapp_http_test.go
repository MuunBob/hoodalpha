package integration

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/httpapi"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
	redisstore "github.com/MuunBob/hoodalpha/internal/persistence/redis"
	"github.com/MuunBob/hoodalpha/internal/telegram/initdata"
)

const miniAppToken = "123456:MINIAPP-HTTP-TEST-TOKEN"

// miniAppUserID matches the id inside signTestInitData's user payload.
const miniAppUserID = domain.TelegramUserID(777001)

// newMiniAppServer builds the real HTTP server with the real middleware and
// verifier, so these tests exercise the deployed authentication path.
func newMiniAppServer(t *testing.T) (http.Handler, *postgres.Pool) {
	t.Helper()
	pool := setupSchema(t)
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	ctx := testContext(t, time.Minute)
	rdb, err := redisstore.Connect(ctx, redisCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	// Clean up the account this suite creates, so runs are repeatable.
	t.Cleanup(func() {
		bg := testContext(t, 30*time.Second)
		_, _ = pool.Exec(bg, `
			DELETE FROM wallets WHERE user_id IN
			    (SELECT user_id FROM telegram_users WHERE telegram_id = $1)`, int64(miniAppUserID))
		_, _ = pool.Exec(bg, `
			DELETE FROM users WHERE id IN
			    (SELECT user_id FROM telegram_users WHERE telegram_id = $1)`, int64(miniAppUserID))
		_, _ = pool.Exec(bg, `DELETE FROM telegram_users WHERE telegram_id = $1`, int64(miniAppUserID))
	})

	verifier, err := initdata.NewVerifier(initdata.Options{
		BotToken: miniAppToken,
		TTL:      15 * time.Minute,
		Guard:    redisstore.NewReplayGuard(rdb, "test-http"),
	})
	if err != nil {
		t.Fatal(err)
	}

	users := postgres.NewUserRepo(pool)
	audit := postgres.NewAuditRepo(pool)
	onboarding := application.NewOnboarding(users, audit,
		domain.NewAllowlist([]domain.TelegramUserID{miniAppUserID}), slog.Default())

	wallets := application.NewWalletService(application.WalletServiceDeps{
		Wallets:       postgres.NewWalletRepo(pool),
		Audit:         audit,
		Chain:         fixedProbe{chainID: 4663, balance: big.NewInt(2_500_000)},
		ChainID:       4663,
		DefaultPolicy: testPolicy(),
		Logger:        slog.Default(),
	})

	srv := httpapi.New(httpapi.Options{
		Addr:   ":0",
		Logger: slog.Default(),
		MiniApp: httpapi.MiniAppDeps{
			Auth:    application.NewMiniAppAuth(verifier, onboarding, audit, slog.Default()),
			Wallets: wallets,
			ChainID: 4663,
		},
	})
	return srv.Handler(), pool
}

func request(t *testing.T, h http.Handler, method, path, initData string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if initData != "" {
		req.Header.Set("X-Telegram-Init-Data", initData)
	}
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMiniAppAuthAcceptsSignedInitData(t *testing.T) {
	h, _ := newMiniAppServer(t)
	raw := signTestInitData(t, miniAppToken, time.Now().UTC())

	rec := request(t, h, http.MethodPost, "/api/miniapp/auth", raw, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["telegram_id"].(float64) != float64(miniAppUserID) {
		t.Errorf("telegram_id = %v, want %d", body["telegram_id"], miniAppUserID)
	}
	if body["user_id"] == "" {
		t.Error("no account id returned")
	}
	// The UI must not be able to imply trading is available.
	if body["trading_available"] != false {
		t.Errorf("trading_available = %v, want false", body["trading_available"])
	}
}

// Every rejection path must return the same opaque 401. Distinguishing them
// would tell an attacker which half of the problem they had solved.
func TestMiniAppRejectsUnauthenticatedRequests(t *testing.T) {
	h, _ := newMiniAppServer(t)

	tests := []struct {
		name     string
		initData string
	}{
		{"missing", ""},
		{"garbage", "not-a-query-string"},
		{"forged signature", signTestInitData(t, "999999:WRONG-TOKEN", time.Now().UTC())},
		{"expired", signTestInitData(t, miniAppToken, time.Now().UTC().Add(-30*time.Minute))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, path := range []string{"/api/miniapp/auth", "/api/miniapp/wallets"} {
				method := http.MethodPost
				if path == "/api/miniapp/wallets" {
					method = http.MethodGet
				}
				rec := request(t, h, method, path, tt.initData, nil)
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("%s %s: status = %d, want 401", method, path, rec.Code)
				}
			}
		})
	}
}

// initData is a credential, so it must not be accepted from the query string
// where it would land in proxy logs and browser history.
func TestMiniAppIgnoresInitDataInQueryString(t *testing.T) {
	h, _ := newMiniAppServer(t)
	raw := signTestInitData(t, miniAppToken, time.Now().UTC())

	req := httptest.NewRequest(http.MethodPost, "/api/miniapp/auth?"+raw, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401: init data must not be read from the URL", rec.Code)
	}
}

func TestMiniAppWalletLifecycle(t *testing.T) {
	h, _ := newMiniAppServer(t)

	// Each request needs a fresh payload: the replay guard refuses reuse.
	newAuth := func() string { return signTestInitData(t, miniAppToken, time.Now().UTC()) }

	// Linking is idempotent from the client's perspective only via 409;
	// start from an empty list.
	rec := request(t, h, http.MethodGet, "/api/miniapp/wallets", newAuth(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}

	const address = "0xAbCdEf0123456789abcDEF0123456789aBCdeF01"
	rec = request(t, h, http.MethodPost, "/api/miniapp/wallets", newAuth(),
		map[string]string{"address": address, "role": "owner", "label": "from mini app"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	walletID, _ := created["id"].(string)
	if walletID == "" {
		t.Fatal("no wallet id returned")
	}
	if created["address"] != "0xabcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("address = %v, want lowercase", created["address"])
	}
	if created["status"] != string(domain.WalletPending) {
		t.Errorf("status = %v, want pending", created["status"])
	}

	// The same address again must be refused rather than duplicated.
	rec = request(t, h, http.MethodPost, "/api/miniapp/wallets", newAuth(),
		map[string]string{"address": address, "role": "owner"})
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate create status = %d, want 409", rec.Code)
	}

	// Policy update.
	rec = request(t, h, http.MethodPut, "/api/miniapp/wallets/"+walletID+"/policy", newAuth(),
		map[string]any{
			"max_position_percent":     7.5,
			"max_open_positions":       3,
			"daily_loss_limit_percent": 8,
			"stop_loss_percent":        6,
			"capital_recovery_percent": 100,
			"max_slippage_bps":         75,
			"min_liquidity_usd":        20000,
			"trading_enabled":          false,
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("policy status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var policy map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	if policy["max_position_percent"].(float64) != 7.5 {
		t.Errorf("max_position_percent = %v, want 7.5", policy["max_position_percent"])
	}

	// A limit outside the safe envelope must be refused by the server, no
	// matter what the client sends.
	rec = request(t, h, http.MethodPut, "/api/miniapp/wallets/"+walletID+"/policy", newAuth(),
		map[string]any{
			"max_position_percent":     500,
			"max_open_positions":       3,
			"daily_loss_limit_percent": 8,
			"stop_loss_percent":        6,
			"capital_recovery_percent": 100,
			"max_slippage_bps":         75,
			"min_liquidity_usd":        20000,
		})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unsafe policy status = %d, want 422", rec.Code)
	}
}

func TestMiniAppRejectsInvalidAddress(t *testing.T) {
	h, _ := newMiniAppServer(t)

	for _, addr := range []string{"nope", "0xdeadbeef", "0x0000000000000000000000000000000000000000"} {
		rec := request(t, h, http.MethodPost, "/api/miniapp/wallets",
			signTestInitData(t, miniAppToken, time.Now().UTC()),
			map[string]string{"address": addr})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("address %q: status = %d, want 400 (body: %s)", addr, rec.Code, rec.Body.String())
		}
	}
}

// A client can name any wallet ID; the server scopes every query to the
// authenticated account. Unknown and not-yours must be indistinguishable, or
// the response becomes an enumeration oracle.
func TestMiniAppCannotReachAnotherUsersWallet(t *testing.T) {
	h, pool := newMiniAppServer(t)
	ctx := testContext(t, time.Minute)

	// A wallet belonging to somebody else entirely.
	otherUser := newTestUser(t, pool)
	other, _ := domain.NewWallet(otherUser,
		"0xfeedfacefeedfacefeedfacefeedfacefeedface", 4663, domain.WalletRoleOwner, "")
	stored, err := postgres.NewWalletRepo(pool).Link(ctx, other, testPolicy())
	if err != nil {
		t.Fatal(err)
	}

	rec := request(t, h, http.MethodGet, "/api/miniapp/wallets/"+stored.ID,
		signTestInitData(t, miniAppToken, time.Now().UTC()), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for another user's wallet", rec.Code)
	}

	rec = request(t, h, http.MethodGet, "/api/miniapp/wallets/00000000-0000-0000-0000-000000000000",
		signTestInitData(t, miniAppToken, time.Now().UTC()), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("nonexistent wallet status = %d, want 404 (same as not-yours)", rec.Code)
	}
}

// The operational endpoints must stay reachable without Mini App credentials.
func TestHealthEndpointsNeedNoInitData(t *testing.T) {
	h, _ := newMiniAppServer(t)

	rec := request(t, h, http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", rec.Code)
	}
}
