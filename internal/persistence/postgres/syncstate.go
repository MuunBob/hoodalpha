package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// SyncStateRepo persists chain progress in chain_sync_state.
type SyncStateRepo struct {
	pool *Pool
}

// NewSyncStateRepo builds the repository.
func NewSyncStateRepo(pool *Pool) *SyncStateRepo { return &SyncStateRepo{pool: pool} }

// SaveHead upserts the observed head. The WHERE clause makes the write
// monotonic: a stale or replayed observation is ignored rather than rewinding
// progress, which matters because task delivery is at-least-once and heads can
// arrive out of order across a reconnect.
func (r *SyncStateRepo) SaveHead(ctx context.Context, chainID uint64, chainName string, block domain.BlockRef) error {
	const q = `
INSERT INTO chain_sync_state (
    chain_id, chain_name, last_block_number, last_block_hash, last_block_time, observed_at, updated_at
) VALUES ($1, $2, $3, $4, $5, now(), now())
ON CONFLICT (chain_id) DO UPDATE SET
    chain_name        = EXCLUDED.chain_name,
    last_block_number = EXCLUDED.last_block_number,
    last_block_hash   = EXCLUDED.last_block_hash,
    last_block_time   = EXCLUDED.last_block_time,
    observed_at       = now(),
    updated_at        = now()
WHERE EXCLUDED.last_block_number > chain_sync_state.last_block_number`

	hash, err := domain.ParseHash(block.Hash.String())
	if err != nil {
		return fmt.Errorf("save head: %w", err)
	}
	_, err = r.pool.Exec(ctx, q,
		int64(chainID), chainName, int64(block.Number), hash.String(), block.Time.UTC())
	if err != nil {
		return fmt.Errorf("save head: %w", err)
	}
	return nil
}

// LoadHead returns the persisted head for a chain. The bool is false when no
// head has been recorded yet.
func (r *SyncStateRepo) LoadHead(ctx context.Context, chainID uint64) (domain.BlockRef, bool, error) {
	const q = `
SELECT last_block_number, last_block_hash, last_block_time
  FROM chain_sync_state
 WHERE chain_id = $1`

	var (
		number int64
		hash   string
		ref    domain.BlockRef
	)
	err := r.pool.QueryRow(ctx, q, int64(chainID)).Scan(&number, &hash, &ref.Time)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BlockRef{}, false, nil
	}
	if err != nil {
		return domain.BlockRef{}, false, fmt.Errorf("load head: %w", err)
	}
	ref.Number = uint64(number)
	ref.Hash = domain.Hash(hash)
	return ref, true, nil
}
