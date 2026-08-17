package domain

import (
	"errors"
	"fmt"
	"time"
)

// Wallet errors that callers distinguish.
var (
	// ErrWalletNotOwned means the wallet belongs to a different account.
	ErrWalletNotOwned = errors.New("wallet not owned by user")
	// ErrWalletAlreadyLinked means the address is already associated.
	ErrWalletAlreadyLinked = errors.New("wallet already linked")
)

// WalletRole distinguishes what a wallet is for.
//
// The owner wallet funds and authorizes; the bot wallet is the only one the
// system will ever sign with, and it holds only the capital intended for
// automated trading. Separating them at the type level keeps a later execution
// phase from accidentally treating a user's main wallet as a signer.
type WalletRole string

const (
	// WalletRoleOwner is the user's own wallet. The bot never holds its keys
	// and never signs for it. Read-only: balances and verification.
	WalletRoleOwner WalletRole = "owner"
	// WalletRoleBot is the dedicated trading wallet.
	WalletRoleBot WalletRole = "bot"
)

// Valid reports whether the role is known.
func (r WalletRole) Valid() bool {
	return r == WalletRoleOwner || r == WalletRoleBot
}

// WalletStatus is the wallet's position in the onboarding state machine.
//
//	PENDING ──verified──▶ ACTIVE ──▶ DISABLED
//	   │                               ▲
//	   └────────── failed ─────────────┘
//
// A wallet is not usable until it has been verified against the chain, so a
// typo or an address from another network cannot silently become a trading
// target.
type WalletStatus string

const (
	// WalletPending is linked but not yet verified on-chain.
	WalletPending WalletStatus = "pending"
	// WalletActive is verified and usable.
	WalletActive WalletStatus = "active"
	// WalletDisabled is retained for history but must not be used.
	WalletDisabled WalletStatus = "disabled"
)

// Valid reports whether the status is known.
func (s WalletStatus) Valid() bool {
	switch s {
	case WalletPending, WalletActive, WalletDisabled:
		return true
	}
	return false
}

// CanTransitionTo reports whether a status change is legal. Modelling this
// explicitly stops a wallet from jumping straight from pending to active
// without passing verification.
func (s WalletStatus) CanTransitionTo(next WalletStatus) bool {
	switch s {
	case WalletPending:
		return next == WalletActive || next == WalletDisabled
	case WalletActive:
		return next == WalletDisabled
	case WalletDisabled:
		// Re-enabling requires re-verification, so it returns to pending.
		return next == WalletPending
	}
	return false
}

// Wallet associates an on-chain address with an account.
//
// It holds no private key, no seed phrase and no secret of any kind. The bot
// stores an address and a policy; signing material belongs to a hardened
// signer introduced in a later phase.
type Wallet struct {
	ID      string
	UserID  UserID
	Address Address
	ChainID uint64
	Role    WalletRole
	Status  WalletStatus
	// Label is an optional operator-facing name.
	Label string
	// VerifiedAt records when the address was confirmed to exist on the
	// expected chain. Nil while pending.
	VerifiedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NewWallet validates and builds a wallet in the pending state.
func NewWallet(userID UserID, address string, chainID uint64, role WalletRole, label string) (Wallet, error) {
	addr, err := ParseAddress(address)
	if err != nil {
		return Wallet{}, err
	}
	// The zero address is a burn target, never a wallet someone controls.
	if addr.IsZero() {
		return Wallet{}, errors.New("refusing to link the zero address")
	}
	if userID == "" {
		return Wallet{}, errors.New("user id is required")
	}
	if chainID == 0 {
		return Wallet{}, errors.New("chain id is required")
	}
	if !role.Valid() {
		return Wallet{}, fmt.Errorf("invalid wallet role %q", role)
	}
	return Wallet{
		UserID:  userID,
		Address: addr,
		ChainID: chainID,
		Role:    role,
		Status:  WalletPending,
		Label:   label,
	}, nil
}

// IsUsable reports whether the wallet may be used for reads and, later, trades.
func (w Wallet) IsUsable() bool { return w.Status == WalletActive }

// WalletPolicy is the per-wallet spending and risk envelope.
//
// The policy is persisted with the wallet so limits survive a restart and are
// enforced server-side. A Telegram message or a Mini App form can request a
// change; neither is trusted to enforce one.
//
// Percentages are stored as NUMERIC in the database, not floating point:
// these values bound how much real money can move.
type WalletPolicy struct {
	ID       string
	WalletID string
	// MaxPositionPercent bounds a single position as a share of capital.
	MaxPositionPercent float64
	// MaxOpenPositions bounds concurrent exposure.
	MaxOpenPositions int
	// DailyLossLimitPercent halts new entries once breached.
	DailyLossLimitPercent float64
	// StopLossPercent is the per-position stop.
	StopLossPercent float64
	// CapitalRecoveryPercent is the gain at which initial capital is recovered.
	CapitalRecoveryPercent float64
	// MaxSlippageBPS bounds acceptable execution slippage.
	MaxSlippageBPS int
	// MinLiquidityUSD rejects illiquid tokens outright.
	MinLiquidityUSD float64
	// TradingEnabled is the per-wallet kill switch. It defaults to false:
	// linking a wallet must never by itself authorise trading.
	TradingEnabled bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Validate rejects a policy that would permit obviously unsafe behaviour.
// These bounds are absolute; a strategy cannot widen them.
func (p WalletPolicy) Validate() error {
	var errs []error
	if p.MaxPositionPercent <= 0 || p.MaxPositionPercent > 100 {
		errs = append(errs, errors.New("max_position_percent must be in (0,100]"))
	}
	if p.MaxOpenPositions < 1 {
		errs = append(errs, errors.New("max_open_positions must be >= 1"))
	}
	if p.DailyLossLimitPercent <= 0 || p.DailyLossLimitPercent > 100 {
		errs = append(errs, errors.New("daily_loss_limit_percent must be in (0,100]"))
	}
	if p.StopLossPercent <= 0 || p.StopLossPercent >= 100 {
		errs = append(errs, errors.New("stop_loss_percent must be in (0,100)"))
	}
	if p.CapitalRecoveryPercent <= 0 {
		errs = append(errs, errors.New("capital_recovery_percent must be > 0"))
	}
	if p.MaxSlippageBPS < 0 || p.MaxSlippageBPS > 10000 {
		errs = append(errs, errors.New("max_slippage_bps must be in [0,10000]"))
	}
	if p.MinLiquidityUSD < 0 {
		errs = append(errs, errors.New("min_liquidity_usd must be >= 0"))
	}
	return errors.Join(errs...)
}
