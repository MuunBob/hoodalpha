package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
)

type stubPinger struct{ err error }

func (s stubPinger) Health(context.Context) error { return s.err }

type stubChain struct {
	id    uint64
	block domain.BlockRef
	err   error
}

func (s stubChain) ChainID(context.Context) (uint64, error) { return s.id, s.err }
func (s stubChain) LatestBlock(context.Context) (domain.BlockRef, error) {
	return s.block, s.err
}

type stubHead struct {
	last      domain.BlockRef
	seen      time.Time
	connected bool
}

func (s stubHead) Last() (domain.BlockRef, time.Time) { return s.last, s.seen }
func (s stubHead) Connected() bool                    { return s.connected }

var testNow = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return testNow }

func component(r domain.HealthReport, name string) (domain.ComponentHealth, bool) {
	for _, c := range r.Components {
		if c.Name == name {
			return c, true
		}
	}
	return domain.ComponentHealth{}, false
}

func TestHealthAllUp(t *testing.T) {
	h := application.NewHealthChecker(application.HealthCheckerDeps{
		Postgres:      stubPinger{},
		Redis:         stubPinger{},
		Chain:         stubChain{id: 4663, block: domain.BlockRef{Number: 100, Time: testNow.Add(-5 * time.Second)}},
		Head:          stubHead{connected: true, last: domain.BlockRef{Number: 100}, seen: testNow.Add(-2 * time.Second)},
		ExpectedChain: 4663,
		StaleAfter:    time.Minute,
		Now:           fixedNow,
	})

	r := h.Check(context.Background())
	if r.Status != domain.HealthUp {
		t.Fatalf("status = %q, want up (components: %+v)", r.Status, r.Components)
	}
	if len(r.Components) != 4 {
		t.Errorf("got %d components, want 4", len(r.Components))
	}
}

func TestHealthPostgresDownFailsWholeReport(t *testing.T) {
	h := application.NewHealthChecker(application.HealthCheckerDeps{
		Postgres:      stubPinger{err: errors.New("connection refused")},
		Redis:         stubPinger{},
		ExpectedChain: 4663,
		Now:           fixedNow,
	})

	r := h.Check(context.Background())
	if r.Status != domain.HealthDown {
		t.Fatalf("status = %q, want down", r.Status)
	}
	pg, ok := component(r, "postgres")
	if !ok {
		t.Fatal("postgres component missing")
	}
	if pg.Status != domain.HealthDown || pg.Error == "" {
		t.Errorf("postgres = %+v, want down with error", pg)
	}
}

// Connecting to the wrong network must be fatal, not a degradation: every
// address, balance and contract read would silently refer to another chain.
func TestHealthWrongChainIsDown(t *testing.T) {
	h := application.NewHealthChecker(application.HealthCheckerDeps{
		Chain:         stubChain{id: 1, block: domain.BlockRef{Number: 1, Time: testNow}},
		ExpectedChain: 4663,
		StaleAfter:    time.Minute,
		Now:           fixedNow,
	})

	r := h.Check(context.Background())
	if r.Status != domain.HealthDown {
		t.Fatalf("status = %q, want down", r.Status)
	}
	c, _ := component(r, "chain_rpc")
	if c.Error != domain.ErrWrongChain.Error() {
		t.Errorf("error = %q, want %q", c.Error, domain.ErrWrongChain)
	}
	if c.Details["expected_chain_id"] != "4663" {
		t.Errorf("expected_chain_id = %q, want 4663", c.Details["expected_chain_id"])
	}
}

// A stale head means the node answers but has stopped producing blocks. The
// process stays alive (degraded) so it can keep monitoring open positions.
func TestHealthStaleBlockIsDegraded(t *testing.T) {
	h := application.NewHealthChecker(application.HealthCheckerDeps{
		Chain:         stubChain{id: 4663, block: domain.BlockRef{Number: 100, Time: testNow.Add(-10 * time.Minute)}},
		ExpectedChain: 4663,
		StaleAfter:    time.Minute,
		Now:           fixedNow,
	})

	r := h.Check(context.Background())
	if r.Status != domain.HealthDegraded {
		t.Fatalf("status = %q, want degraded", r.Status)
	}
}

func TestHealthWebSocketStates(t *testing.T) {
	tests := []struct {
		name string
		head stubHead
		want domain.HealthStatus
	}{
		{"disconnected", stubHead{connected: false}, domain.HealthDown},
		{"connected but no head yet", stubHead{connected: true}, domain.HealthDegraded},
		{"connected and fresh", stubHead{connected: true, seen: testNow.Add(-time.Second)}, domain.HealthUp},
		{"connected but silent", stubHead{connected: true, seen: testNow.Add(-10 * time.Minute)}, domain.HealthDegraded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := application.NewHealthChecker(application.HealthCheckerDeps{
				Head:       tt.head,
				StaleAfter: time.Minute,
				Now:        fixedNow,
			})
			r := h.Check(context.Background())
			c, ok := component(r, "chain_ws")
			if !ok {
				t.Fatal("chain_ws component missing")
			}
			if c.Status != tt.want {
				t.Errorf("status = %q, want %q (error: %q)", c.Status, tt.want, c.Error)
			}
		})
	}
}

// A binary that does not run a dependency must not invent a component for it.
func TestHealthOmitsAbsentDependencies(t *testing.T) {
	h := application.NewHealthChecker(application.HealthCheckerDeps{
		Redis: stubPinger{},
		Now:   fixedNow,
	})
	r := h.Check(context.Background())
	if len(r.Components) != 1 {
		t.Fatalf("got %d components, want 1", len(r.Components))
	}
	if r.Components[0].Name != "redis" {
		t.Errorf("component = %q, want redis", r.Components[0].Name)
	}
}
