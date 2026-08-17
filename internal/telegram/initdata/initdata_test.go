package initdata_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/telegram/initdata"
)

const testToken = "123456:TEST-BOT-TOKEN-not-a-real-secret"

var testNow = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return testNow }

// signInitData builds a correctly signed payload, following the same algorithm
// Telegram documents. Building it independently of the implementation means a
// bug in the verifier cannot be cancelled out by the same bug in the fixture.
func signInitData(t *testing.T, token string, fields map[string]string) string {
	t.Helper()

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var pairs []string
	for _, k := range keys {
		pairs = append(pairs, k+"="+fields[k])
	}
	checkString := strings.Join(pairs, "\n")

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))

	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(checkString))
	hash := hex.EncodeToString(mac.Sum(nil))

	values := url.Values{}
	for k, v := range fields {
		values.Set(k, v)
	}
	values.Set("hash", hash)
	return values.Encode()
}

func validFields(authDate time.Time) map[string]string {
	return map[string]string{
		"auth_date": strconv.FormatInt(authDate.Unix(), 10),
		"query_id":  "AAHdF6IQAAAAAN0Xohc",
		"user":      `{"id":42,"first_name":"Test","username":"tester","language_code":"en"}`,
	}
}

func newVerifier(t *testing.T, guard initdata.ReplayGuard) *initdata.Verifier {
	t.Helper()
	v, err := initdata.NewVerifier(initdata.Options{
		BotToken: testToken,
		TTL:      15 * time.Minute,
		Guard:    guard,
		Now:      fixedNow,
	})
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	return v
}

func TestVerifyAcceptsValidInitData(t *testing.T) {
	raw := signInitData(t, testToken, validFields(testNow.Add(-time.Minute)))

	data, err := newVerifier(t, nil).Verify(raw)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if data.User.ID != 42 {
		t.Errorf("user id = %d, want 42", data.User.ID)
	}
	if data.User.Username != "tester" {
		t.Errorf("username = %q, want tester", data.User.Username)
	}
	if data.Hash == "" {
		t.Error("verified hash is empty; replay protection needs it as a key")
	}
}

// The central property: a payload signed with any other token must be refused.
// Without this, anyone could mint their own init data and impersonate a user.
func TestVerifyRejectsWrongToken(t *testing.T) {
	raw := signInitData(t, "999999:SOME-OTHER-BOT-TOKEN", validFields(testNow))

	_, err := newVerifier(t, nil).Verify(raw)
	if !errors.Is(err, initdata.ErrInvalidSignature) {
		t.Fatalf("error = %v, want ErrInvalidSignature", err)
	}
}

// Tampering with any signed field must invalidate the hash. This is what stops
// a user from taking their own valid payload and editing the user ID inside it.
func TestVerifyRejectsTamperedFields(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{"elevated user id", "user", `{"id":1,"first_name":"Admin"}`},
		{"extended auth date", "auth_date", strconv.FormatInt(testNow.Add(time.Hour).Unix(), 10)},
		{"swapped query id", "query_id", "forged"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := signInitData(t, testToken, validFields(testNow))

			values, err := url.ParseQuery(raw)
			if err != nil {
				t.Fatal(err)
			}
			values.Set(tt.field, tt.value) // hash left untouched

			_, err = newVerifier(t, nil).Verify(values.Encode())
			if !errors.Is(err, initdata.ErrInvalidSignature) {
				t.Errorf("error = %v, want ErrInvalidSignature", err)
			}
		})
	}
}

func TestVerifyRejectsExpiredInitData(t *testing.T) {
	// Signed 20 minutes ago against a 15-minute TTL.
	raw := signInitData(t, testToken, validFields(testNow.Add(-20*time.Minute)))

	_, err := newVerifier(t, nil).Verify(raw)
	if !errors.Is(err, initdata.ErrExpired) {
		t.Fatalf("error = %v, want ErrExpired", err)
	}
}

// A far-future auth_date would otherwise stay inside the TTL forever, turning
// a captured payload into a permanent credential.
func TestVerifyRejectsFutureAuthDate(t *testing.T) {
	raw := signInitData(t, testToken, validFields(testNow.Add(time.Hour)))

	_, err := newVerifier(t, nil).Verify(raw)
	if !errors.Is(err, initdata.ErrFutureAuthDate) {
		t.Fatalf("error = %v, want ErrFutureAuthDate", err)
	}
}

// Small clock differences between Telegram and this server are normal and must
// not reject a legitimate user.
func TestVerifyToleratesSmallClockSkew(t *testing.T) {
	raw := signInitData(t, testToken, validFields(testNow.Add(20*time.Second)))

	if _, err := newVerifier(t, nil).Verify(raw); err != nil {
		t.Fatalf("Verify() rejected a payload 20s ahead: %v", err)
	}
}

func TestVerifyRejectsMalformedPayloads(t *testing.T) {
	v := newVerifier(t, nil)

	tests := []struct {
		name string
		raw  string
		want error
	}{
		{"empty", "", initdata.ErrMalformed},
		{"no hash", "auth_date=1&user=%7B%22id%22%3A1%7D", initdata.ErrMissingHash},
		{"non-hex hash", "hash=zzzz&auth_date=1", initdata.ErrInvalidSignature},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := v.Verify(tt.raw); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestVerifyRequiresUser(t *testing.T) {
	fields := map[string]string{
		"auth_date": strconv.FormatInt(testNow.Unix(), 10),
		"query_id":  "AAHdF6IQ",
	}
	raw := signInitData(t, testToken, fields)

	_, err := newVerifier(t, nil).Verify(raw)
	if !errors.Is(err, initdata.ErrMissingUser) {
		t.Fatalf("error = %v, want ErrMissingUser", err)
	}
}

// memoryGuard is an in-process ReplayGuard for tests.
type memoryGuard struct {
	mu   sync.Mutex
	seen map[string]bool
	err  error
}

func newMemoryGuard() *memoryGuard { return &memoryGuard{seen: map[string]bool{}} }

func (g *memoryGuard) FirstUse(hash string, _ time.Duration) (bool, error) {
	if g.err != nil {
		return false, g.err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen[hash] {
		return false, nil
	}
	g.seen[hash] = true
	return true, nil
}

// A signature stays valid for its whole TTL, so without a replay guard a
// captured payload is reusable until it expires.
func TestVerifyBlocksReplay(t *testing.T) {
	guard := newMemoryGuard()
	v := newVerifier(t, guard)
	raw := signInitData(t, testToken, validFields(testNow))

	if _, err := v.Verify(raw); err != nil {
		t.Fatalf("first Verify() error = %v", err)
	}
	_, err := v.Verify(raw)
	if !errors.Is(err, initdata.ErrReplayed) {
		t.Fatalf("second Verify() error = %v, want ErrReplayed", err)
	}
}

// If the guard is unavailable the request must fail, not silently proceed with
// replay protection disabled.
func TestVerifyFailsClosedWhenGuardUnavailable(t *testing.T) {
	guard := newMemoryGuard()
	guard.err = errors.New("redis is down")

	raw := signInitData(t, testToken, validFields(testNow))
	if _, err := newVerifier(t, guard).Verify(raw); err == nil {
		t.Fatal("Verify() succeeded while the replay guard was unavailable")
	}
}

// An invalid payload must not consume a guard entry: otherwise an attacker
// could burn entries, and a legitimate retry after a network blip would be
// misreported as a replay.
func TestInvalidPayloadDoesNotConsumeGuard(t *testing.T) {
	guard := newMemoryGuard()
	v := newVerifier(t, guard)

	bad := signInitData(t, "another:token", validFields(testNow))
	if _, err := v.Verify(bad); err == nil {
		t.Fatal("expected rejection")
	}
	if len(guard.seen) != 0 {
		t.Errorf("guard recorded %d entries for a rejected payload", len(guard.seen))
	}
}

func TestNewVerifierRequiresToken(t *testing.T) {
	if _, err := initdata.NewVerifier(initdata.Options{}); err == nil {
		t.Fatal("NewVerifier() succeeded without a bot token")
	}
}

// The signature field is Ed25519 for third-party verification and is not part
// of the HMAC input. Including it would break otherwise-valid payloads.
func TestVerifyIgnoresSignatureFieldInHMAC(t *testing.T) {
	fields := validFields(testNow)
	raw := signInitData(t, testToken, fields)

	values, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	values.Set("signature", "some-ed25519-signature-value")

	if _, err := newVerifier(t, nil).Verify(values.Encode()); err != nil {
		t.Fatalf("Verify() failed when a signature field was present: %v", err)
	}
}
