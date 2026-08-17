package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// AuditRepo appends to the audit trail.
//
// Writes go to timeseries.audit_logs, which pg_partman partitions by month.
// The table is append-only by convention: history is never edited to correct a
// mistake, only added to.
type AuditRepo struct {
	pool *Pool
}

// NewAuditRepo builds the repository.
func NewAuditRepo(pool *Pool) *AuditRepo { return &AuditRepo{pool: pool} }

// Record appends one event.
//
// Detail is serialised as JSON. Callers must not place secrets in it — the
// audit trail is read during incidents and shipped to logs.
func (r *AuditRepo) Record(ctx context.Context, e domain.AuditEvent) error {
	detail := []byte("{}")
	if len(e.Detail) > 0 {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return fmt.Errorf("marshal audit detail: %w", err)
		}
		detail = b
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO timeseries.audit_logs
		    (occurred_at, actor_type, actor_id, action, subject_type, subject_id, outcome, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)`,
		e.OccurredAt.UTC(), e.ActorType, nullable(e.ActorID), e.Action,
		nullable(e.SubjectType), nullable(e.SubjectID), e.Outcome, string(detail))
	if err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
}

// ListByAction returns recent events for one action, newest first. Used by
// tests and operator queries; the DESC index on occurred_at serves it.
func (r *AuditRepo) ListByAction(ctx context.Context, action string, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT occurred_at, actor_type, coalesce(actor_id, ''), action,
		       coalesce(subject_type, ''), coalesce(subject_id, ''), outcome, detail
		  FROM timeseries.audit_logs
		 WHERE action = $1
		 ORDER BY occurred_at DESC
		 LIMIT $2`, action, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	var out []domain.AuditEvent
	for rows.Next() {
		var (
			e   domain.AuditEvent
			raw []byte
		)
		if err := rows.Scan(&e.OccurredAt, &e.ActorType, &e.ActorID, &e.Action,
			&e.SubjectType, &e.SubjectID, &e.Outcome, &raw); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
