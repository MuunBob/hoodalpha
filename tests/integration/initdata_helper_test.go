package integration

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// queryIDSeq makes each generated payload unique. Telegram issues a distinct
// query_id per Mini App launch, and the replay guard keys on the resulting
// hash — so a fixture that reused one would be refused as a replay rather
// than testing what the caller intended.
var queryIDSeq atomic.Int64

// signTestInitData builds a signed Mini App payload, following the algorithm
// from https://core.telegram.org/bots/webapps. It is implemented here
// independently of the verifier so a bug in one is not cancelled by the other.
func signTestInitData(t *testing.T, token string, authDate time.Time) string {
	t.Helper()

	fields := map[string]string{
		"auth_date": strconv.FormatInt(authDate.Unix(), 10),
		"query_id":  "AAHdF6IQ-" + strconv.FormatInt(queryIDSeq.Add(1), 10),
		"user":      `{"id":777001,"first_name":"Integration","username":"integration","language_code":"en"}`,
	}

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+fields[k])
	}

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))

	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(pairs, "\n")))

	values := url.Values{}
	for k, v := range fields {
		values.Set(k, v)
	}
	values.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return values.Encode()
}
