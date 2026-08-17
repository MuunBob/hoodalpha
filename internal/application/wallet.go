package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// WalletService links wallets to accounts and verifies them against the chain.
//
// It stores addresses and policies. It has no signer, requests no key material,
// and there is no code path here that could accept one.
type WalletService struct {
	wallets WalletStore
	audit   AuditStore
	chain   AddressProbe
	queue   TaskEnqueuer
	chainID uint64
	// defaults seed a new wallet's policy from configured risk settings, so
	// limits exist from the moment a wallet is linked.
	defaults domain.WalletPolicy
	log      *slog.Logger
	now      func() time.Time
}

// WalletServiceDeps wires a WalletService.
type WalletServiceDeps struct {
	Wallets       WalletStore
	Audit         AuditStore
	Chain         AddressProbe
	Queue         TaskEnqueuer
	ChainID       uint64
	DefaultPolicy domain.WalletPolicy
	Logger        *slog.Logger
	Now           func() time.Time
}

// NewWalletService builds the use case.
func NewWalletService(d WalletServiceDeps) *WalletService {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return &WalletService{
		wallets:  d.Wallets,
		audit:    d.Audit,
		chain:    d.Chain,
		queue:    d.Queue,
		chainID:  d.ChainID,
		defaults: d.DefaultPolicy,
		log:      d.Logger.With("component", "wallet"),
		now:      d.Now,
	}
}

// LinkRequest asks to associate an address with an account.
type LinkRequest struct {
	UserID  domain.UserID
	Address string
	Role    domain.WalletRole
	Label   string
	// ActorID identifies who requested this, for the audit trail.
	ActorID string
}

// Link validates an address and records it in the pending state.
//
// The wallet is deliberately NOT active on return: it becomes usable only
// after on-chain verification confirms the address exists on the expected
// chain. A typo, or an address copied from another network, must not silently
// become a trading target. Verification is enqueued rather than performed
// inline so a slow or failing RPC does not block the user's request.
func (s *WalletService) Link(ctx context.Context, req LinkRequest) (domain.Wallet, error) {
	wallet, err := domain.NewWallet(req.UserID, req.Address, s.chainID, req.Role, req.Label)
	if err != nil {
		s.recordAudit(ctx, domain.NewAuditEvent(domain.ActorTelegramUser, req.ActorID, domain.ActionWalletLinked).
			WithOutcome(domain.OutcomeRejected).
			WithDetail("reason", err.Error()))
		return domain.Wallet{}, err
	}

	policy := s.defaults
	// Trading is never enabled by linking. Enabling it is a separate,
	// explicitly audited decision.
	policy.TradingEnabled = false

	stored, err := s.wallets.Link(ctx, wallet, policy)
	if err != nil {
		outcome := domain.OutcomeFailed
		if errors.Is(err, domain.ErrWalletAlreadyLinked) {
			outcome = domain.OutcomeRejected
		}
		s.recordAudit(ctx, domain.NewAuditEvent(domain.ActorTelegramUser, req.ActorID, domain.ActionWalletLinked).
			WithSubject("wallet_address", wallet.Address.String()).
			WithOutcome(outcome).
			WithDetail("reason", err.Error()))
		return domain.Wallet{}, err
	}

	s.log.Info("wallet linked",
		"wallet_id", stored.ID, "address", stored.Address.String(),
		"chain_id", stored.ChainID, "role", string(stored.Role))
	s.recordAudit(ctx, domain.NewAuditEvent(domain.ActorTelegramUser, req.ActorID, domain.ActionWalletLinked).
		WithSubject("wallet", stored.ID).
		WithDetail("address", stored.Address.String()).
		WithDetail("chain_id", stored.ChainID).
		WithDetail("role", string(stored.Role)))

	if s.queue != nil {
		if err := s.queue.EnqueueWalletVerify(ctx, stored.ID); err != nil {
			// The wallet stays pending and a later manual or scheduled
			// verification can still promote it, so this is not fatal.
			s.log.Error("could not enqueue wallet verification", "wallet_id", stored.ID, "error", err)
		}
	}
	return stored, nil
}

// Verify confirms the wallet's address on the expected chain and activates it.
//
// Idempotent: the underlying update only promotes a pending wallet, so a
// repeated task delivery cannot resurrect a wallet an operator has disabled.
func (s *WalletService) Verify(ctx context.Context, walletID string) (domain.Wallet, error) {
	wallet, err := s.wallets.Get(ctx, walletID)
	if err != nil {
		return domain.Wallet{}, err
	}

	// Confirm we are talking to the network the wallet claims to be on.
	// Verifying an address against the wrong chain proves nothing.
	id, err := s.chain.ChainID(ctx)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("read chain id: %w", err)
	}
	if id != wallet.ChainID {
		s.recordAudit(ctx, domain.NewAuditEvent(domain.ActorWorker, "worker", domain.ActionWalletVerifyFailed).
			WithSubject("wallet", walletID).
			WithOutcome(domain.OutcomeFailed).
			WithDetail("reason", "chain id mismatch").
			WithDetail("node_chain_id", id).
			WithDetail("wallet_chain_id", wallet.ChainID))
		return domain.Wallet{}, fmt.Errorf("%w: node reports %d, wallet expects %d",
			domain.ErrWrongChain, id, wallet.ChainID)
	}

	// Reading the balance proves the node can resolve the address on this
	// chain. It intentionally does not require a non-zero balance: an unfunded
	// wallet is a normal starting state, not an invalid one.
	balance, err := s.chain.BalanceAt(ctx, wallet.Address, nil)
	if err != nil {
		s.recordAudit(ctx, domain.NewAuditEvent(domain.ActorWorker, "worker", domain.ActionWalletVerifyFailed).
			WithSubject("wallet", walletID).
			WithOutcome(domain.OutcomeFailed).
			WithDetail("reason", "balance read failed"))
		return domain.Wallet{}, fmt.Errorf("read balance: %w", err)
	}

	if err := s.wallets.MarkVerified(ctx, walletID, s.now()); err != nil {
		return domain.Wallet{}, err
	}

	s.log.Info("wallet verified",
		"wallet_id", walletID, "address", wallet.Address.String(),
		"balance_wei", balance.String())
	s.recordAudit(ctx, domain.NewAuditEvent(domain.ActorWorker, "worker", domain.ActionWalletVerified).
		WithSubject("wallet", walletID).
		WithDetail("address", wallet.Address.String()).
		WithDetail("chain_id", wallet.ChainID))

	return s.wallets.Get(ctx, walletID)
}

// WalletView is a wallet plus its policy and current balance, for display.
type WalletView struct {
	Wallet  domain.Wallet
	Policy  domain.WalletPolicy
	Balance domain.Wei
	// BalanceError explains why a balance is missing, so the UI can show the
	// wallet rather than failing the whole request over one RPC hiccup.
	BalanceError string
}

// List returns a user's wallets with policies and balances.
func (s *WalletService) List(ctx context.Context, userID domain.UserID) ([]WalletView, error) {
	wallets, err := s.wallets.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	out := make([]WalletView, 0, len(wallets))
	for _, w := range wallets {
		view := WalletView{Wallet: w}
		if p, err := s.wallets.GetPolicy(ctx, w.ID); err == nil {
			view.Policy = p
		}
		if s.chain != nil {
			bal, err := s.chain.BalanceAt(ctx, w.Address, nil)
			if err != nil {
				view.BalanceError = err.Error()
			} else {
				view.Balance = bal
			}
		}
		out = append(out, view)
	}
	return out, nil
}

// Get returns one wallet, enforcing ownership.
func (s *WalletService) Get(ctx context.Context, userID domain.UserID, walletID string) (WalletView, error) {
	w, err := s.wallets.Get(ctx, walletID)
	if err != nil {
		return WalletView{}, err
	}
	// Ownership is checked server-side on every read. A Mini App client can
	// send any wallet ID it likes; it is not trusted to scope its own query.
	if w.UserID != userID {
		return WalletView{}, domain.ErrWalletNotOwned
	}

	view := WalletView{Wallet: w}
	if p, err := s.wallets.GetPolicy(ctx, w.ID); err == nil {
		view.Policy = p
	}
	if s.chain != nil {
		if bal, err := s.chain.BalanceAt(ctx, w.Address, nil); err != nil {
			view.BalanceError = err.Error()
		} else {
			view.Balance = bal
		}
	}
	return view, nil
}

// UpdatePolicy changes a wallet's limits after checking ownership.
func (s *WalletService) UpdatePolicy(ctx context.Context, userID domain.UserID, walletID string, p domain.WalletPolicy, actorID string) (domain.WalletPolicy, error) {
	w, err := s.wallets.Get(ctx, walletID)
	if err != nil {
		return domain.WalletPolicy{}, err
	}
	if w.UserID != userID {
		return domain.WalletPolicy{}, domain.ErrWalletNotOwned
	}

	// Validate here rather than relying on the store to do it. These bounds
	// guard real money, so they are enforced by the use case (this check),
	// the repository, and database constraints — any one of the three being
	// bypassed or reimplemented must not silently widen a limit.
	if err := p.Validate(); err != nil {
		s.recordAudit(ctx, domain.NewAuditEvent(domain.ActorTelegramUser, actorID, domain.ActionPolicyChanged).
			WithSubject("wallet", walletID).
			WithOutcome(domain.OutcomeRejected).
			WithDetail("reason", err.Error()))
		return domain.WalletPolicy{}, fmt.Errorf("invalid policy: %w", err)
	}

	before, err := s.wallets.GetPolicy(ctx, walletID)
	if err != nil {
		return domain.WalletPolicy{}, err
	}

	if err := s.wallets.UpdatePolicy(ctx, walletID, p); err != nil {
		s.recordAudit(ctx, domain.NewAuditEvent(domain.ActorTelegramUser, actorID, domain.ActionPolicyChanged).
			WithSubject("wallet", walletID).
			WithOutcome(domain.OutcomeRejected).
			WithDetail("reason", err.Error()))
		return domain.WalletPolicy{}, err
	}

	// Record what changed, not just that something did: during an incident the
	// question is always which limit moved and when.
	s.recordAudit(ctx, domain.NewAuditEvent(domain.ActorTelegramUser, actorID, domain.ActionPolicyChanged).
		WithSubject("wallet", walletID).
		WithDetail("before", policyDetail(before)).
		WithDetail("after", policyDetail(p)))

	s.log.Info("wallet policy updated", "wallet_id", walletID,
		"trading_enabled", p.TradingEnabled)
	return s.wallets.GetPolicy(ctx, walletID)
}

// Disable turns a wallet off. Used by an operator revoking access.
func (s *WalletService) Disable(ctx context.Context, userID domain.UserID, walletID, actorID string) error {
	w, err := s.wallets.Get(ctx, walletID)
	if err != nil {
		return err
	}
	if w.UserID != userID {
		return domain.ErrWalletNotOwned
	}
	if err := s.wallets.SetStatus(ctx, walletID, domain.WalletDisabled); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.NewAuditEvent(domain.ActorTelegramUser, actorID, domain.ActionWalletDisabled).
		WithSubject("wallet", walletID))
	return nil
}

func policyDetail(p domain.WalletPolicy) map[string]any {
	return map[string]any{
		"max_position_percent":     p.MaxPositionPercent,
		"max_open_positions":       p.MaxOpenPositions,
		"daily_loss_limit_percent": p.DailyLossLimitPercent,
		"stop_loss_percent":        p.StopLossPercent,
		"capital_recovery_percent": p.CapitalRecoveryPercent,
		"max_slippage_bps":         p.MaxSlippageBPS,
		"min_liquidity_usd":        p.MinLiquidityUSD,
		"trading_enabled":          p.TradingEnabled,
	}
}

func (s *WalletService) recordAudit(ctx context.Context, e domain.AuditEvent) {
	if s.audit == nil {
		return
	}
	if err := s.audit.Record(ctx, e); err != nil {
		s.log.Error("audit write failed", "action", e.Action, "error", err)
	}
}
