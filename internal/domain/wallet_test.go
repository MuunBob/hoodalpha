package domain_test

import (
	"testing"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

const testAddress = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"

func TestNewWalletNormalisesAddress(t *testing.T) {
	w, err := domain.NewWallet("user-1", testAddress, 4663, domain.WalletRoleOwner, "main")
	if err != nil {
		t.Fatalf("NewWallet() error = %v", err)
	}
	if w.Address.String() != "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed" {
		t.Errorf("address = %q, want lowercase form", w.Address)
	}
	// A wallet must never start usable: it has not been seen on-chain yet.
	if w.Status != domain.WalletPending {
		t.Errorf("status = %q, want pending", w.Status)
	}
	if w.IsUsable() {
		t.Error("a freshly linked wallet reports as usable")
	}
}

func TestNewWalletRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		userID  domain.UserID
		address string
		chainID uint64
		role    domain.WalletRole
	}{
		{"not an address", "user-1", "nope", 4663, domain.WalletRoleOwner},
		{"too short", "user-1", "0xdeadbeef", 4663, domain.WalletRoleOwner},
		{"empty user", "", testAddress, 4663, domain.WalletRoleOwner},
		{"zero chain", "user-1", testAddress, 0, domain.WalletRoleOwner},
		{"unknown role", "user-1", testAddress, 4663, domain.WalletRole("signer")},
		// The zero address is a burn target, never a wallet anyone controls.
		{"zero address", "user-1", "0x0000000000000000000000000000000000000000", 4663, domain.WalletRoleOwner},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := domain.NewWallet(tt.userID, tt.address, tt.chainID, tt.role, ""); err == nil {
				t.Error("NewWallet() accepted invalid input")
			}
		})
	}
}

// The state machine is explicit so a wallet cannot become active without
// passing verification.
func TestWalletStatusTransitions(t *testing.T) {
	tests := []struct {
		from domain.WalletStatus
		to   domain.WalletStatus
		want bool
	}{
		{domain.WalletPending, domain.WalletActive, true},
		{domain.WalletPending, domain.WalletDisabled, true},
		{domain.WalletActive, domain.WalletDisabled, true},
		// Re-enabling requires re-verification, so it returns to pending.
		{domain.WalletDisabled, domain.WalletPending, true},
		{domain.WalletDisabled, domain.WalletActive, false},
		{domain.WalletActive, domain.WalletPending, false},
	}

	for _, tt := range tests {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
			t.Errorf("%s -> %s = %t, want %t", tt.from, tt.to, got, tt.want)
		}
	}
}

func validPolicy() domain.WalletPolicy {
	return domain.WalletPolicy{
		MaxPositionPercent:     5,
		MaxOpenPositions:       5,
		DailyLossLimitPercent:  10,
		StopLossPercent:        5,
		CapitalRecoveryPercent: 100,
		MaxSlippageBPS:         100,
		MinLiquidityUSD:        10000,
	}
}

func TestWalletPolicyValidation(t *testing.T) {
	if err := validPolicy().Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*domain.WalletPolicy)
	}{
		{"position over 100%", func(p *domain.WalletPolicy) { p.MaxPositionPercent = 101 }},
		{"position zero", func(p *domain.WalletPolicy) { p.MaxPositionPercent = 0 }},
		{"negative position", func(p *domain.WalletPolicy) { p.MaxPositionPercent = -5 }},
		{"no open positions", func(p *domain.WalletPolicy) { p.MaxOpenPositions = 0 }},
		{"daily loss over 100%", func(p *domain.WalletPolicy) { p.DailyLossLimitPercent = 150 }},
		{"stop loss at 100%", func(p *domain.WalletPolicy) { p.StopLossPercent = 100 }},
		{"zero capital recovery", func(p *domain.WalletPolicy) { p.CapitalRecoveryPercent = 0 }},
		{"slippage over 100%", func(p *domain.WalletPolicy) { p.MaxSlippageBPS = 10001 }},
		{"negative slippage", func(p *domain.WalletPolicy) { p.MaxSlippageBPS = -1 }},
		{"negative liquidity", func(p *domain.WalletPolicy) { p.MinLiquidityUSD = -1 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPolicy()
			tt.mutate(&p)
			if err := p.Validate(); err == nil {
				t.Error("Validate() accepted an unsafe policy")
			}
		})
	}
}

// Linking a wallet must never by itself authorise trading.
func TestPolicyTradingDisabledByDefault(t *testing.T) {
	var p domain.WalletPolicy
	if p.TradingEnabled {
		t.Error("zero-value policy has trading enabled")
	}
}

func TestAllowlist(t *testing.T) {
	list := domain.NewAllowlist([]domain.TelegramUserID{1001, 1002})

	if !list.Allows(1001) {
		t.Error("configured id was refused")
	}
	if list.Allows(9999) {
		t.Error("unconfigured id was allowed")
	}
	if list.Size() != 2 {
		t.Errorf("size = %d, want 2", list.Size())
	}

	// An unconfigured deployment must be closed, not open to everyone.
	empty := domain.NewAllowlist(nil)
	if !empty.Empty() {
		t.Error("empty allowlist does not report as empty")
	}
	if empty.Allows(1001) {
		t.Error("empty allowlist admitted a user")
	}

	// Invalid IDs are dropped rather than admitted.
	if domain.NewAllowlist([]domain.TelegramUserID{0, -5}).Size() != 0 {
		t.Error("allowlist accepted a non-positive id")
	}
}

func TestTelegramUserDisplayName(t *testing.T) {
	tests := []struct {
		name string
		user domain.TelegramUser
		want string
	}{
		{"username", domain.TelegramUser{Username: "tester"}, "@tester"},
		{"full name", domain.TelegramUser{FirstName: "Ada", LastName: "Lovelace"}, "Ada Lovelace"},
		{"first only", domain.TelegramUser{FirstName: "Ada"}, "Ada"},
		{"nothing", domain.TelegramUser{TelegramID: 42}, "user 42"},
	}
	for _, tt := range tests {
		if got := tt.user.DisplayName(); got != tt.want {
			t.Errorf("%s: DisplayName() = %q, want %q", tt.name, got, tt.want)
		}
	}
}
