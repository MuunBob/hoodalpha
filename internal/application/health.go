// Package application holds use cases. It orchestrates infrastructure adapters
// behind narrow interfaces and owns no transport or storage details itself.
package application

import (
	"context"
	"sync"
	"time"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// Pinger is any dependency that can confirm it is reachable.
type Pinger interface {
	Health(ctx context.Context) error
}

// ChainProbe reports chain identity and head progress.
type ChainProbe interface {
	ChainID(ctx context.Context) (uint64, error)
	LatestBlock(ctx context.Context) (domain.BlockRef, error)
}

// HeadWatcher exposes the WebSocket subscriber's view of the chain head.
type HeadWatcher interface {
	Last() (domain.BlockRef, time.Time)
	Connected() bool
}

// HealthChecker runs every dependency probe and aggregates the result.
type HealthChecker struct {
	postgres      Pinger
	redis         Pinger
	chain         ChainProbe
	head          HeadWatcher
	expectedChain uint64
	staleAfter    time.Duration
	version       string
	now           func() time.Time
}

// HealthCheckerDeps wires a HealthChecker. Any dependency may be nil, in which
// case it is simply not reported — the worker has no HTTP server, the API has
// no head subscriber, and neither should fake a component it does not run.
type HealthCheckerDeps struct {
	Postgres      Pinger
	Redis         Pinger
	Chain         ChainProbe
	Head          HeadWatcher
	ExpectedChain uint64
	StaleAfter    time.Duration
	Version       string
	// Now is injectable so tests can control staleness without sleeping.
	Now func() time.Time
}

// NewHealthChecker builds a checker from its dependencies.
func NewHealthChecker(d HealthCheckerDeps) *HealthChecker {
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	if d.StaleAfter <= 0 {
		d.StaleAfter = 2 * time.Minute
	}
	return &HealthChecker{
		postgres:      d.Postgres,
		redis:         d.Redis,
		chain:         d.Chain,
		head:          d.Head,
		expectedChain: d.ExpectedChain,
		staleAfter:    d.StaleAfter,
		version:       d.Version,
		now:           d.Now,
	}
}

// Check probes all dependencies concurrently and returns the aggregate report.
func (h *HealthChecker) Check(ctx context.Context) domain.HealthReport {
	type job struct {
		name string
		fn   func(context.Context) domain.ComponentHealth
	}
	var jobs []job

	if h.postgres != nil {
		jobs = append(jobs, job{"postgres", func(ctx context.Context) domain.ComponentHealth {
			return pingComponent(ctx, "postgres", h.postgres, h.now)
		}})
	}
	if h.redis != nil {
		jobs = append(jobs, job{"redis", func(ctx context.Context) domain.ComponentHealth {
			return pingComponent(ctx, "redis", h.redis, h.now)
		}})
	}
	if h.chain != nil {
		jobs = append(jobs, job{"chain_rpc", h.checkChain})
	}
	if h.head != nil {
		jobs = append(jobs, job{"chain_ws", h.checkHead})
	}

	results := make([]domain.ComponentHealth, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			results[i] = j.fn(ctx)
		}(i, j)
	}
	wg.Wait()

	return domain.HealthReport{
		Status:     domain.Aggregate(results),
		CheckedAt:  h.now(),
		Version:    h.version,
		Components: results,
	}
}

func pingComponent(ctx context.Context, name string, p Pinger, now func() time.Time) domain.ComponentHealth {
	start := now()
	err := p.Health(ctx)
	c := domain.ComponentHealth{Name: name, Status: domain.HealthUp}
	c.Latency = now().Sub(start)
	c.LatencyMS = c.Latency.Milliseconds()
	if err != nil {
		c.Status = domain.HealthDown
		c.Error = err.Error()
	}
	return c
}

func (h *HealthChecker) checkChain(ctx context.Context) domain.ComponentHealth {
	start := h.now()
	c := domain.ComponentHealth{Name: "chain_rpc", Status: domain.HealthUp, Details: map[string]string{}}
	defer func() {
		c.Latency = h.now().Sub(start)
		c.LatencyMS = c.Latency.Milliseconds()
	}()

	id, err := h.chain.ChainID(ctx)
	if err != nil {
		c.Status = domain.HealthDown
		c.Error = err.Error()
		return c
	}
	c.Details["chain_id"] = itoa(id)
	c.Details["chain_name"] = domain.ChainName(id)

	// Connecting to the wrong network is a hard failure, not a degradation:
	// every downstream address and balance would be meaningless.
	if h.expectedChain != 0 && id != h.expectedChain {
		c.Status = domain.HealthDown
		c.Error = domain.ErrWrongChain.Error()
		c.Details["expected_chain_id"] = itoa(h.expectedChain)
		return c
	}

	block, err := h.chain.LatestBlock(ctx)
	if err != nil {
		c.Status = domain.HealthDown
		c.Error = err.Error()
		return c
	}
	c.Details["last_block"] = itoa(block.Number)
	c.Details["last_block_time"] = block.Time.UTC().Format(time.RFC3339)
	if block.IsStale(h.now(), h.staleAfter) {
		c.Status = domain.HealthDegraded
		c.Error = "chain head is stale"
	}
	return c
}

func (h *HealthChecker) checkHead(ctx context.Context) domain.ComponentHealth {
	c := domain.ComponentHealth{Name: "chain_ws", Status: domain.HealthUp, Details: map[string]string{}}

	last, seenAt := h.head.Last()
	c.Details["connected"] = btoa(h.head.Connected())

	if !h.head.Connected() {
		c.Status = domain.HealthDown
		c.Error = "websocket not connected"
		return c
	}
	if seenAt.IsZero() {
		// Connected but no head has arrived yet: normal right after startup.
		c.Status = domain.HealthDegraded
		c.Error = "no block head received yet"
		return c
	}
	c.Details["last_block"] = itoa(last.Number)
	c.Details["last_head_at"] = seenAt.UTC().Format(time.RFC3339)
	if h.now().Sub(seenAt) > h.staleAfter {
		c.Status = domain.HealthDegraded
		c.Error = "no block head received recently"
	}
	return c
}

func itoa[T ~uint64](v T) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}

func btoa(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
