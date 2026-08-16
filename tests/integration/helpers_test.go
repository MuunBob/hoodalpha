// Package integration holds tests that talk to real infrastructure.
//
// They are skipped unless the relevant endpoint is configured, so `go test ./...`
// stays green on a machine without Docker. Run `make up` first, then
// `make test-integration`. Nothing here is mocked: a passing run means the real
// dependency answered.
package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/config"
)

const (
	envPostgres = "TEST_POSTGRES_URL"
	envRedis    = "TEST_REDIS_ADDR"
	envRPC      = "TEST_RH_RPC_URL"
	envWS       = "TEST_RH_WS_URL"
	envChainID  = "TEST_RH_CHAIN_ID"
)

// postgresConfig returns the test database config, skipping if unset.
func postgresConfig(t *testing.T) config.PostgresConfig {
	t.Helper()
	url := os.Getenv(envPostgres)
	if url == "" {
		t.Skipf("%s not set; run `make up` and export it to enable this test", envPostgres)
	}
	return config.PostgresConfig{
		URL:             url,
		MaxConns:        4,
		MinConns:        0,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
		ConnectTimeout:  10 * time.Second,
	}
}

// redisConfig returns the test Redis config, skipping if unset.
func redisConfig(t *testing.T) config.RedisConfig {
	t.Helper()
	addr := os.Getenv(envRedis)
	if addr == "" {
		t.Skipf("%s not set; run `make up` and export it to enable this test", envRedis)
	}
	return config.RedisConfig{
		Addr:     addr,
		Password: os.Getenv("TEST_REDIS_PASSWORD"),
		// A dedicated database keeps test queues out of the development ones.
		DB:       redisTestDB(),
		PoolSize: 4,
	}
}

func redisTestDB() int {
	if v := os.Getenv("TEST_REDIS_DB"); v != "" {
		var n int
		for _, c := range v {
			if c < '0' || c > '9' {
				return 15
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return 15
}

// rpcURL returns the chain RPC endpoint, skipping if unset.
func rpcURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv(envRPC)
	if url == "" {
		t.Skipf("%s not set; export it to enable chain integration tests", envRPC)
	}
	return url
}

// wsURL returns the chain WebSocket endpoint, skipping if unset.
func wsURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv(envWS)
	if url == "" {
		t.Skipf("%s not set; export it to enable websocket tests", envWS)
	}
	return url
}

// expectedChainID is the chain the RPC endpoint must report.
func expectedChainID() uint64 {
	if v := os.Getenv(envChainID); v != "" {
		var n uint64
		for _, c := range v {
			if c < '0' || c > '9' {
				return 4663
			}
			n = n*10 + uint64(c-'0')
		}
		return n
	}
	return 4663
}

// testContext returns a context bounded by the test deadline.
func testContext(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
