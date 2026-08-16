package application

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// SyncStateStore persists the highest block the bot has observed.
type SyncStateStore interface {
	SaveHead(ctx context.Context, chainID uint64, chainName string, block domain.BlockRef) error
	LoadHead(ctx context.Context, chainID uint64) (domain.BlockRef, bool, error)
}

// ChainSync records chain progress so a restart knows where it left off.
//
// It only advances: an out-of-order or replayed head never rewinds the stored
// value, which keeps the operation safe under at-least-once task delivery.
type ChainSync struct {
	chain   ChainProbe
	store   SyncStateStore
	chainID uint64
	log     *slog.Logger
}

// NewChainSync wires a ChainSync.
func NewChainSync(chain ChainProbe, store SyncStateStore, chainID uint64, log *slog.Logger) *ChainSync {
	if log == nil {
		log = slog.Default()
	}
	return &ChainSync{chain: chain, store: store, chainID: chainID, log: log.With("component", "chain_sync")}
}

// RecordLatest fetches the current head and persists it.
func (s *ChainSync) RecordLatest(ctx context.Context) (domain.BlockRef, error) {
	block, err := s.chain.LatestBlock(ctx)
	if err != nil {
		return domain.BlockRef{}, fmt.Errorf("latest block: %w", err)
	}
	if err := s.Record(ctx, block); err != nil {
		return domain.BlockRef{}, err
	}
	return block, nil
}

// Record persists a specific head observation.
func (s *ChainSync) Record(ctx context.Context, block domain.BlockRef) error {
	if err := s.store.SaveHead(ctx, s.chainID, domain.ChainName(s.chainID), block); err != nil {
		return fmt.Errorf("save head: %w", err)
	}
	return nil
}

// LastSeen returns the persisted head, if any. Used at startup to report how
// far behind the bot is before it resumes work.
func (s *ChainSync) LastSeen(ctx context.Context) (domain.BlockRef, bool, error) {
	return s.store.LoadHead(ctx, s.chainID)
}
