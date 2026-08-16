package domain_test

import (
	"math/big"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

func TestParseAddress(t *testing.T) {
	tests := []struct {
		in      string
		want    domain.Address
		wantErr bool
	}{
		{in: "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", want: "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed"},
		{in: "  0xABCDEF0123456789abcdef0123456789ABCDEF01  ", want: "0xabcdef0123456789abcdef0123456789abcdef01"},
		{in: "0x0000000000000000000000000000000000000000", want: "0x0000000000000000000000000000000000000000"},
		{in: "5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", wantErr: true},  // no 0x
		{in: "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAe", wantErr: true}, // 39 nibbles
		{in: "0xZZAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", wantErr: true},
		{in: "", wantErr: true},
	}

	for _, tt := range tests {
		got, err := domain.ParseAddress(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseAddress(%q) = %q, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseAddress(%q) error = %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseAddress(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAddressIsZero(t *testing.T) {
	zero, _ := domain.ParseAddress("0x0000000000000000000000000000000000000000")
	if !zero.IsZero() {
		t.Error("zero address IsZero() = false")
	}
	nonZero, _ := domain.ParseAddress("0x0000000000000000000000000000000000000001")
	if nonZero.IsZero() {
		t.Error("non-zero address IsZero() = true")
	}
}

func TestParseHash(t *testing.T) {
	valid := "0xAABBCCDDEEFF00112233445566778899aabbccddeeff00112233445566778899"
	got, err := domain.ParseHash(valid)
	if err != nil {
		t.Fatalf("ParseHash error = %v", err)
	}
	want := domain.Hash("0xaabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	if got != want {
		t.Errorf("ParseHash = %q, want %q", got, want)
	}

	for _, bad := range []string{"0xdeadbeef", "", "not-a-hash", valid + "00"} {
		if _, err := domain.ParseHash(bad); err == nil {
			t.Errorf("ParseHash(%q) succeeded, want error", bad)
		}
	}
}

func TestChainName(t *testing.T) {
	if got := domain.ChainName(domain.ChainIDMainnet); got != "robinhood-mainnet" {
		t.Errorf("ChainName(4663) = %q", got)
	}
	if got := domain.ChainName(domain.ChainIDTestnet); got != "robinhood-testnet" {
		t.Errorf("ChainName(46630) = %q", got)
	}
	if got := domain.ChainName(1); got != "chain-1" {
		t.Errorf("ChainName(1) = %q", got)
	}
}

func TestBlockRefIsStale(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	fresh := domain.BlockRef{Number: 10, Time: now.Add(-30 * time.Second)}
	if fresh.IsStale(now, time.Minute) {
		t.Error("30s-old block reported stale with 1m threshold")
	}

	old := domain.BlockRef{Number: 10, Time: now.Add(-5 * time.Minute)}
	if !old.IsStale(now, time.Minute) {
		t.Error("5m-old block not reported stale with 1m threshold")
	}

	// A zero time means we have never seen a block; treat that as stale so
	// health never reports "up" on a connection that produced nothing.
	var never domain.BlockRef
	if !never.IsStale(now, time.Hour) {
		t.Error("zero-time block not reported stale")
	}
}

func TestWeiExactArithmetic(t *testing.T) {
	// 1.5 ETH. Chosen because float64 cannot hold 1.5e18 + 1 exactly, so a
	// float-backed implementation would silently lose the trailing wei.
	raw, _ := new(big.Int).SetString("1500000000000000001", 10)
	w := domain.NewWei(raw)

	if got := w.String(); got != "1500000000000000001" {
		t.Errorf("Wei.String() = %s, want 1500000000000000001", got)
	}
	if got := w.Ether(); got != "1.500000000000000001" {
		t.Errorf("Wei.Ether() = %s, want 1.500000000000000001", got)
	}
}

func TestWeiDefensiveCopy(t *testing.T) {
	raw := big.NewInt(1000)
	w := domain.NewWei(raw)

	raw.SetInt64(9999) // mutate the caller's value after construction
	if w.String() != "1000" {
		t.Errorf("Wei captured a reference: got %s, want 1000", w.String())
	}

	out := w.BigInt()
	out.SetInt64(1) // mutate the returned value
	if w.String() != "1000" {
		t.Errorf("BigInt() leaked internal state: got %s, want 1000", w.String())
	}
}

func TestWeiZeroAndNegative(t *testing.T) {
	if !domain.NewWei(nil).IsZero() {
		t.Error("NewWei(nil) is not zero")
	}
	// Native balances are never negative; clamp rather than propagate nonsense.
	if !domain.NewWei(big.NewInt(-5)).IsZero() {
		t.Error("NewWei(-5) is not clamped to zero")
	}
	var w domain.Wei
	if !w.IsZero() || w.String() != "0" {
		t.Error("zero-value Wei is not usable")
	}
	if got := domain.NewWei(big.NewInt(0)).Ether(); got != "0" {
		t.Errorf("zero Ether() = %q, want 0", got)
	}
}

func TestAggregateHealth(t *testing.T) {
	up := domain.ComponentHealth{Status: domain.HealthUp}
	degraded := domain.ComponentHealth{Status: domain.HealthDegraded}
	down := domain.ComponentHealth{Status: domain.HealthDown}

	tests := []struct {
		name string
		in   []domain.ComponentHealth
		want domain.HealthStatus
	}{
		{"empty", nil, domain.HealthUp},
		{"all up", []domain.ComponentHealth{up, up}, domain.HealthUp},
		{"one degraded", []domain.ComponentHealth{up, degraded}, domain.HealthDegraded},
		{"one down", []domain.ComponentHealth{up, down}, domain.HealthDown},
		{"down beats degraded", []domain.ComponentHealth{degraded, down}, domain.HealthDown},
	}
	for _, tt := range tests {
		if got := domain.Aggregate(tt.in); got != tt.want {
			t.Errorf("%s: Aggregate = %q, want %q", tt.name, got, tt.want)
		}
	}
}
