// Package initdata verifies Telegram Mini App init data server-side.
//
// The Mini App is an untrusted UI client. Telegram exposes both `initData`
// (a signed query string) and `initDataUnsafe` (a pre-parsed object). Only
// initData carries a signature, so only initData is verified here — the name
// "unsafe" is Telegram's own warning that the parsed form proves nothing.
//
// Verification follows https://core.telegram.org/bots/webapps:
//
//	secret_key      = HMAC_SHA256(key: "WebAppData", data: bot_token)
//	data_check_str  = every field except `hash` and `signature`,
//	                  sorted by key, joined "k=v" with \n
//	expected_hash   = hex(HMAC_SHA256(key: secret_key, data: data_check_str))
//
// A valid signature only proves Telegram produced the payload at some point.
// It says nothing about when, so auth_date is checked against a TTL, and
// replay is blocked separately by the Verifier's ReplayGuard.
package initdata

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Verification failures. Callers distinguish these to audit the reason without
// echoing the payload.
var (
	// ErrMissingHash means no signature was present at all.
	ErrMissingHash = errors.New("init data has no hash")
	// ErrInvalidSignature means the payload was not produced by this bot's token.
	ErrInvalidSignature = errors.New("init data signature is invalid")
	// ErrExpired means the payload is older than the configured TTL.
	ErrExpired = errors.New("init data has expired")
	// ErrFutureAuthDate means auth_date is implausibly ahead of the server clock.
	ErrFutureAuthDate = errors.New("init data auth_date is in the future")
	// ErrMissingAuthDate means the payload carried no timestamp to check.
	ErrMissingAuthDate = errors.New("init data has no auth_date")
	// ErrMissingUser means the payload identified no user.
	ErrMissingUser = errors.New("init data has no user")
	// ErrReplayed means this exact payload was already accepted.
	ErrReplayed = errors.New("init data has already been used")
	// ErrMalformed means the payload is not a parseable query string.
	ErrMalformed = errors.New("init data is malformed")
)

// User is the Telegram user described by init data.
type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
	IsPremium    bool   `json:"is_premium"`
	PhotoURL     string `json:"photo_url"`
}

// Data is verified init data. A value of this type has passed signature,
// freshness and replay checks; an unverified payload never becomes one.
type Data struct {
	User       User
	AuthDate   time.Time
	QueryID    string
	ChatType   string
	ChatID     int64
	StartParam string
	// Hash is the verified signature, used as the replay key.
	Hash string
}

// ReplayGuard records which payloads have already been accepted.
//
// A signature stays valid for its whole TTL, so an attacker who captures one
// init data string could otherwise replay it repeatedly within that window.
// Implementations are expected to be shared across processes (Redis), because
// a per-process guard would not stop a replay hitting another replica.
type ReplayGuard interface {
	// FirstUse atomically records the hash and reports whether this call was
	// the first. It must be atomic: a check-then-set race is exactly the
	// window a replay would exploit.
	FirstUse(hash string, ttl time.Duration) (bool, error)
}

// Verifier validates init data for one bot token.
type Verifier struct {
	// secretKey is HMAC_SHA256("WebAppData", botToken), derived once at
	// construction so the raw token is not re-handled on every request.
	secretKey []byte
	ttl       time.Duration
	guard     ReplayGuard
	now       func() time.Time
	// clockSkew tolerates a small amount of clock drift between Telegram and
	// this server before rejecting a future-dated payload.
	clockSkew time.Duration
}

// Options configure a Verifier.
type Options struct {
	// BotToken is the token from BotFather. It is used only to derive the
	// secret key and is never stored on the Verifier.
	BotToken string
	// TTL bounds how old init data may be. Telegram suggests checking
	// auth_date; a short window limits the value of a captured payload.
	TTL time.Duration
	// Guard blocks replays. Optional but strongly recommended: without it a
	// captured payload is reusable for the whole TTL.
	Guard ReplayGuard
	// Now is injectable for tests.
	Now func() time.Time
	// ClockSkew tolerance for future-dated payloads. Defaults to 1 minute.
	ClockSkew time.Duration
}

// NewVerifier builds a Verifier.
func NewVerifier(opts Options) (*Verifier, error) {
	if opts.BotToken == "" {
		return nil, errors.New("bot token is required to verify init data")
	}
	if opts.TTL <= 0 {
		opts.TTL = 24 * time.Hour
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.ClockSkew <= 0 {
		opts.ClockSkew = time.Minute
	}

	// secret_key = HMAC_SHA256(key: "WebAppData", data: bot_token)
	mac := hmac.New(sha256.New, []byte("WebAppData"))
	mac.Write([]byte(opts.BotToken))

	return &Verifier{
		secretKey: mac.Sum(nil),
		ttl:       opts.TTL,
		guard:     opts.Guard,
		now:       opts.Now,
		clockSkew: opts.ClockSkew,
	}, nil
}

// Verify checks a raw initData query string and returns the parsed payload.
//
// It fails closed: any error means the caller must treat the request as
// unauthenticated. Errors never include the payload, which carries user data.
func (v *Verifier) Verify(raw string) (Data, error) {
	if strings.TrimSpace(raw) == "" {
		return Data{}, ErrMalformed
	}

	values, err := url.ParseQuery(raw)
	if err != nil {
		return Data{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}

	providedHash := values.Get("hash")
	if providedHash == "" {
		return Data{}, ErrMissingHash
	}

	// Build the data-check string: every field except hash and signature,
	// sorted by key, joined with newlines. `signature` is excluded because it
	// is the Ed25519 third-party signature, not part of the HMAC input.
	keys := make([]string, 0, len(values))
	for k := range values {
		if k == "hash" || k == "signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(values.Get(k))
	}

	mac := hmac.New(sha256.New, v.secretKey)
	mac.Write([]byte(b.String()))
	expected := mac.Sum(nil)

	provided, err := hex.DecodeString(providedHash)
	if err != nil {
		return Data{}, ErrInvalidSignature
	}
	// Constant time: a length-independent comparison would leak how much of
	// the hash a forgery got right.
	if !hmac.Equal(expected, provided) {
		return Data{}, ErrInvalidSignature
	}

	// Signature is valid, but that only proves Telegram produced this payload
	// at some point — not that it is recent.
	authDateRaw := values.Get("auth_date")
	if authDateRaw == "" {
		return Data{}, ErrMissingAuthDate
	}
	authUnix, err := strconv.ParseInt(authDateRaw, 10, 64)
	if err != nil {
		return Data{}, fmt.Errorf("%w: auth_date is not a unix timestamp", ErrMalformed)
	}
	authDate := time.Unix(authUnix, 0).UTC()

	now := v.now()
	if now.Sub(authDate) > v.ttl {
		return Data{}, ErrExpired
	}
	// A far-future auth_date would otherwise stay "fresh" indefinitely.
	if authDate.Sub(now) > v.clockSkew {
		return Data{}, ErrFutureAuthDate
	}

	userRaw := values.Get("user")
	if userRaw == "" {
		return Data{}, ErrMissingUser
	}
	var user User
	if err := json.Unmarshal([]byte(userRaw), &user); err != nil {
		return Data{}, fmt.Errorf("%w: user is not valid json", ErrMalformed)
	}
	if user.ID <= 0 {
		return Data{}, ErrMissingUser
	}

	data := Data{
		User:       user,
		AuthDate:   authDate,
		QueryID:    values.Get("query_id"),
		ChatType:   values.Get("chat_type"),
		StartParam: values.Get("start_param"),
		Hash:       providedHash,
	}
	if raw := values.Get("chat_instance"); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			data.ChatID = id
		}
	}

	// Replay check runs last: an invalid or stale payload should not consume
	// a guard entry, and the guard is the most expensive step.
	if v.guard != nil {
		// The entry outlives the TTL slightly so a payload cannot be replayed
		// in the gap between the guard forgetting it and the TTL rejecting it.
		first, err := v.guard.FirstUse(providedHash, v.ttl+time.Minute)
		if err != nil {
			return Data{}, fmt.Errorf("replay check: %w", err)
		}
		if !first {
			return Data{}, ErrReplayed
		}
	}

	return data, nil
}

// Telegram's Ed25519 public keys for third-party verification, from
// https://core.telegram.org/bots/webapps. Verifying with these needs no bot
// token, which is why a third party can check a payload without holding one.
const (
	// PublicKeyProduction verifies payloads from the production environment.
	PublicKeyProduction = "e7bf03a2fa4602af4580703d88dda5bb59f32ed8b02a56c187fe7d34caed242d"
	// PublicKeyTest verifies payloads from Telegram's test environment.
	PublicKeyTest = "40055058a4ee38156a06562e52eece92a771bcd8346a8c4615cb7376eddf72ec"
)

// VerifyThirdParty checks the Ed25519 `signature` field instead of the HMAC
// `hash`, using Telegram's published public key and the bot's numeric ID.
//
// This exists so a component that must not hold the bot token can still
// validate a payload. The data-check string differs from the HMAC form: it is
// prefixed with "<bot_id>:WebAppData".
func VerifyThirdParty(raw string, botID int64, publicKeyHex string) (Data, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return Data{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}

	sigRaw := values.Get("signature")
	if sigRaw == "" {
		return Data{}, ErrMissingHash
	}
	// Telegram base64url-encodes the signature, usually unpadded.
	sig, err := decodeBase64URL(sigRaw)
	if err != nil {
		return Data{}, ErrInvalidSignature
	}

	pub, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return Data{}, errors.New("invalid telegram public key")
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		if k == "hash" || k == "signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(strconv.FormatInt(botID, 10))
	b.WriteString(":WebAppData")
	for _, k := range keys {
		b.WriteByte('\n')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(values.Get(k))
	}

	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(b.String()), sig) {
		return Data{}, ErrInvalidSignature
	}

	authDateRaw := values.Get("auth_date")
	if authDateRaw == "" {
		return Data{}, ErrMissingAuthDate
	}
	authUnix, err := strconv.ParseInt(authDateRaw, 10, 64)
	if err != nil {
		return Data{}, fmt.Errorf("%w: auth_date is not a unix timestamp", ErrMalformed)
	}

	var user User
	if userRaw := values.Get("user"); userRaw != "" {
		if err := json.Unmarshal([]byte(userRaw), &user); err != nil {
			return Data{}, fmt.Errorf("%w: user is not valid json", ErrMalformed)
		}
	}
	if user.ID <= 0 {
		return Data{}, ErrMissingUser
	}

	return Data{
		User:       user,
		AuthDate:   time.Unix(authUnix, 0).UTC(),
		QueryID:    values.Get("query_id"),
		ChatType:   values.Get("chat_type"),
		StartParam: values.Get("start_param"),
		Hash:       values.Get("hash"),
	}, nil
}
