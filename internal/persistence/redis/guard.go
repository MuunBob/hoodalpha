package redis

import (
	"context"
	"fmt"
	"time"
)

// ReplayGuard blocks a signed payload from being accepted twice.
//
// It lives in Redis rather than process memory because a replay that hits a
// different replica must also be refused; an in-process map would only protect
// whichever instance happened to see the original.
type ReplayGuard struct {
	client *Client
	prefix string
}

// NewReplayGuard builds a guard. prefix namespaces the keys.
func NewReplayGuard(client *Client, prefix string) *ReplayGuard {
	if prefix == "" {
		prefix = "replay"
	}
	return &ReplayGuard{client: client, prefix: prefix}
}

// FirstUse records the hash and reports whether this call was the first.
//
// SETNX is atomic, which matters: a check-then-set would leave exactly the
// race window a replay attack needs.
func (g *ReplayGuard) FirstUse(hash string, ttl time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ok, err := g.client.SetNX(ctx, g.key(hash), "1", ttl).Result()
	if err != nil {
		// Fail closed. If the guard is unavailable we cannot prove this
		// payload is fresh, and accepting it would silently disable replay
		// protection exactly when Redis is having a bad day.
		return false, fmt.Errorf("replay guard unavailable: %w", err)
	}
	return ok, nil
}

func (g *ReplayGuard) key(hash string) string {
	return g.prefix + ":initdata:" + hash
}

// RateLimiter is a fixed-window counter keyed by identity.
//
// Fixed window rather than a token bucket: it needs one round trip, the burst
// behaviour at a window edge is acceptable for chat commands, and there is no
// state to reconcile after a restart.
type RateLimiter struct {
	client *Client
	prefix string
}

// NewRateLimiter builds a limiter.
func NewRateLimiter(client *Client, prefix string) *RateLimiter {
	if prefix == "" {
		prefix = "ratelimit"
	}
	return &RateLimiter{client: client, prefix: prefix}
}

// Allow reports whether the identity may perform another action in this window,
// along with how many remain.
func (r *RateLimiter) Allow(ctx context.Context, identity string, limit int, window time.Duration) (bool, int, error) {
	if limit <= 0 {
		return false, 0, nil
	}
	key := fmt.Sprintf("%s:%s:%d", r.prefix, identity, time.Now().UTC().Truncate(window).Unix())

	pipe := r.client.TxPipeline()
	incr := pipe.Incr(ctx, key)
	// Expire on every call is harmless and avoids a key leaking if the process
	// dies between INCR and EXPIRE.
	pipe.Expire(ctx, key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		// Fail open: a Redis outage must not lock the operator out of the
		// control plane. Rate limiting protects against noise, not against a
		// compromised identity — the allowlist does that, and it is checked
		// independently against PostgreSQL.
		return true, 0, fmt.Errorf("rate limiter unavailable: %w", err)
	}

	count := int(incr.Val())
	remaining := limit - count
	if remaining < 0 {
		remaining = 0
	}
	return count <= limit, remaining, nil
}
