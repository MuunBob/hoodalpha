// Package tasks holds the Asynq handlers. Handlers are thin: they decode a
// payload and call a use case. Business logic lives in internal/application.
package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/queue"
)

// HealthCheckPayload is the body of a system:health_check task.
type HealthCheckPayload struct {
	// RequestedAt lets a handler notice it is processing a stale scheduled task.
	RequestedAt time.Time `json:"requested_at"`
}

// HealthCheck probes every dependency and logs the result. Idempotent: it only
// reads, so a duplicate delivery costs one extra probe and nothing else.
func HealthCheck(checker *application.HealthChecker, log *slog.Logger) queue.Handler {
	log = log.With("task", queue.TypeSystemHealthCheck)
	return func(ctx context.Context, payload []byte) error {
		var p HealthCheckPayload
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &p); err != nil {
				// A malformed payload will never parse; retrying wastes slots.
				return queue.SkipRetry(fmt.Errorf("decode payload: %w", err))
			}
		}

		report := checker.Check(ctx)
		attrs := []any{"status", string(report.Status)}
		for _, c := range report.Components {
			attrs = append(attrs, c.Name, string(c.Status))
		}
		switch report.Status {
		case domain.HealthDown:
			log.Error("health check failed", attrs...)
			// Returning an error makes the failure visible in Asynqmon's retry
			// queue rather than silently succeeding.
			return fmt.Errorf("health status %s", report.Status)
		case domain.HealthDegraded:
			log.Warn("health check degraded", attrs...)
		default:
			log.Info("health check ok", attrs...)
		}
		return nil
	}
}

// SyncHeadPayload is the body of a chain:sync_head task.
type SyncHeadPayload struct {
	RequestedAt time.Time `json:"requested_at"`
}

// SyncHead reads the current chain head and persists it.
//
// Idempotent by construction: the repository write only advances the stored
// block number, so replaying an old task cannot rewind sync progress.
func SyncHead(sync *application.ChainSync, log *slog.Logger) queue.Handler {
	log = log.With("task", queue.TypeChainSyncHead)
	return func(ctx context.Context, payload []byte) error {
		var p SyncHeadPayload
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &p); err != nil {
				return queue.SkipRetry(fmt.Errorf("decode payload: %w", err))
			}
		}

		block, err := sync.RecordLatest(ctx)
		if err != nil {
			return err
		}
		log.Info("chain head recorded", "block", block.Number, "hash", block.Hash.String())
		return nil
	}
}
