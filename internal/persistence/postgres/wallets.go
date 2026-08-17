package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// WalletRepo persists wallets and their policies.
//
// It stores addresses and limits. There is no method to store a private key,
// because there is no column for one.
type WalletRepo struct {
	pool *Pool
}

// NewWalletRepo builds the repository.
func NewWalletRepo(pool *Pool) *WalletRepo { return &WalletRepo{pool: pool} }

const walletSelect = `
SELECT id::text, user_id::text, address, chain_id, role, status,
       coalesce(label, ''), verified_at, created_at, updated_at
  FROM wallets`

// Link creates a wallet in the pending state together with its policy, in one
// transaction. A wallet without a policy would be a wallet with no limits, so
// the two are never written separately.
func (r *WalletRepo) Link(ctx context.Context, w domain.Wallet, p domain.WalletPolicy) (domain.Wallet, error) {
	if err := p.Validate(); err != nil {
		return domain.Wallet{}, fmt.Errorf("invalid policy: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO wallets (user_id, address, chain_id, role, status, label)
		VALUES ($1, $2, $3, $4, 'pending', $5)
		RETURNING id::text`,
		w.UserID.String(), w.Address.String(), int64(w.ChainID), string(w.Role), nullable(w.Label)).Scan(&id)
	if isUniqueViolation(err) {
		return domain.Wallet{}, domain.ErrWalletAlreadyLinked
	}
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("insert wallet: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO wallet_policies (
		    wallet_id, max_position_percent, max_open_positions, daily_loss_limit_percent,
		    stop_loss_percent, capital_recovery_percent, max_slippage_bps,
		    min_liquidity_usd, trading_enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, p.MaxPositionPercent, p.MaxOpenPositions, p.DailyLossLimitPercent,
		p.StopLossPercent, p.CapitalRecoveryPercent, p.MaxSlippageBPS,
		p.MinLiquidityUSD, p.TradingEnabled); err != nil {
		return domain.Wallet{}, fmt.Errorf("insert policy: %w", err)
	}

	stored, err := scanWallet(tx.QueryRow(ctx, walletSelect+` WHERE id = $1`, id))
	if err != nil {
		return domain.Wallet{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Wallet{}, fmt.Errorf("commit: %w", err)
	}
	return stored, nil
}

// Get loads one wallet by ID.
func (r *WalletRepo) Get(ctx context.Context, id string) (domain.Wallet, error) {
	return scanWallet(r.pool.QueryRow(ctx, walletSelect+` WHERE id = $1`, id))
}

// ListByUser returns a user's wallets, newest first.
func (r *WalletRepo) ListByUser(ctx context.Context, userID domain.UserID) ([]domain.Wallet, error) {
	rows, err := r.pool.Query(ctx, walletSelect+` WHERE user_id = $1 ORDER BY created_at DESC`, userID.String())
	if err != nil {
		return nil, fmt.Errorf("list wallets: %w", err)
	}
	defer rows.Close()

	var out []domain.Wallet
	for rows.Next() {
		w, err := scanWallet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// MarkVerified moves a wallet from pending to active.
//
// The status guard in the WHERE clause makes this idempotent and ordering-safe:
// re-running a verification task cannot resurrect a wallet an operator has
// since disabled.
func (r *WalletRepo) MarkVerified(ctx context.Context, id string, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE wallets
		   SET status = 'active', verified_at = $2
		 WHERE id = $1 AND status = 'pending'`, id, at.UTC())
	if err != nil {
		return fmt.Errorf("mark verified: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Already active, or disabled. Not an error: the task is retry-safe.
		return nil
	}
	return nil
}

// SetStatus changes a wallet's status, rejecting illegal transitions.
func (r *WalletRepo) SetStatus(ctx context.Context, id string, next domain.WalletStatus) error {
	if !next.Valid() {
		return fmt.Errorf("invalid wallet status %q", next)
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.Status == next {
		return nil
	}
	if !current.Status.CanTransitionTo(next) {
		return fmt.Errorf("illegal wallet transition %s -> %s", current.Status, next)
	}
	// Returning to pending means re-verification, so the old proof is cleared.
	clearVerified := next == domain.WalletPending
	_, err = r.pool.Exec(ctx, `
		UPDATE wallets
		   SET status = $2,
		       verified_at = CASE WHEN $3 THEN NULL ELSE verified_at END
		 WHERE id = $1`, id, string(next), clearVerified)
	if err != nil {
		return fmt.Errorf("set wallet status: %w", err)
	}
	return nil
}

const policySelect = `
SELECT id::text, wallet_id::text, max_position_percent, max_open_positions,
       daily_loss_limit_percent, stop_loss_percent, capital_recovery_percent,
       max_slippage_bps, min_liquidity_usd, trading_enabled, created_at, updated_at
  FROM wallet_policies`

// GetPolicy loads a wallet's policy.
func (r *WalletRepo) GetPolicy(ctx context.Context, walletID string) (domain.WalletPolicy, error) {
	var p domain.WalletPolicy
	err := r.pool.QueryRow(ctx, policySelect+` WHERE wallet_id = $1`, walletID).
		Scan(&p.ID, &p.WalletID, &p.MaxPositionPercent, &p.MaxOpenPositions,
			&p.DailyLossLimitPercent, &p.StopLossPercent, &p.CapitalRecoveryPercent,
			&p.MaxSlippageBPS, &p.MinLiquidityUSD, &p.TradingEnabled,
			&p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WalletPolicy{}, ErrNotFound
	}
	if err != nil {
		return domain.WalletPolicy{}, fmt.Errorf("get policy: %w", err)
	}
	return p, nil
}

// UpdatePolicy replaces a wallet's limits. The policy is validated before it
// reaches the database, and the database re-checks the same bounds: a limit
// that guards real money is worth asserting twice.
func (r *WalletRepo) UpdatePolicy(ctx context.Context, walletID string, p domain.WalletPolicy) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("invalid policy: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE wallet_policies
		   SET max_position_percent = $2, max_open_positions = $3,
		       daily_loss_limit_percent = $4, stop_loss_percent = $5,
		       capital_recovery_percent = $6, max_slippage_bps = $7,
		       min_liquidity_usd = $8, trading_enabled = $9
		 WHERE wallet_id = $1`,
		walletID, p.MaxPositionPercent, p.MaxOpenPositions, p.DailyLossLimitPercent,
		p.StopLossPercent, p.CapitalRecoveryPercent, p.MaxSlippageBPS,
		p.MinLiquidityUSD, p.TradingEnabled)
	if err != nil {
		return fmt.Errorf("update policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanWallet(row rowScanner) (domain.Wallet, error) {
	var (
		w          domain.Wallet
		verifiedAt *time.Time
	)
	err := row.Scan(&w.ID, &w.UserID, &w.Address, &w.ChainID, &w.Role, &w.Status,
		&w.Label, &verifiedAt, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Wallet{}, ErrNotFound
	}
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("scan wallet: %w", err)
	}
	w.VerifiedAt = verifiedAt
	return w, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
