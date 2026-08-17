package domain

import "time"

// Audit actor types. Who caused an event.
const (
	ActorSystem       = "system"
	ActorTelegramUser = "telegram_user"
	ActorWorker       = "worker"
	ActorOperator     = "operator"
)

// Audit outcomes.
const (
	OutcomeOK       = "ok"
	OutcomeRejected = "rejected"
	OutcomeFailed   = "failed"
)

// Audit actions. Every security-relevant event gets a stable name so the trail
// stays queryable as the code changes.
const (
	ActionTelegramStart        = "telegram:start"
	ActionTelegramCommand      = "telegram:command"
	ActionTelegramUnauthorized = "telegram:unauthorized"
	ActionTelegramRateLimited  = "telegram:rate_limited"
	ActionUserOnboarded        = "user:onboarded"
	ActionMiniAppAuth          = "miniapp:auth"
	ActionMiniAppAuthRejected  = "miniapp:auth_rejected"
	ActionWalletLinked         = "wallet:linked"
	ActionWalletVerified       = "wallet:verified"
	ActionWalletVerifyFailed   = "wallet:verify_failed"
	ActionWalletDisabled       = "wallet:disabled"
	ActionPolicyChanged        = "policy:changed"
)

// AuditEvent is one append-only record of something security-relevant.
//
// Detail must never contain a private key, seed phrase, bot token or raw
// initData. The audit trail is read during incidents and shipped to logs;
// a secret written here is a secret leaked.
type AuditEvent struct {
	OccurredAt  time.Time
	ActorType   string
	ActorID     string
	Action      string
	SubjectType string
	SubjectID   string
	Outcome     string
	Detail      map[string]any
}

// NewAuditEvent builds an event with the current time.
func NewAuditEvent(actorType, actorID, action string) AuditEvent {
	return AuditEvent{
		OccurredAt: time.Now().UTC(),
		ActorType:  actorType,
		ActorID:    actorID,
		Action:     action,
		Outcome:    OutcomeOK,
		Detail:     map[string]any{},
	}
}

// WithSubject records what the action was performed on.
func (e AuditEvent) WithSubject(subjectType, subjectID string) AuditEvent {
	e.SubjectType = subjectType
	e.SubjectID = subjectID
	return e
}

// WithOutcome sets the result.
func (e AuditEvent) WithOutcome(outcome string) AuditEvent {
	e.Outcome = outcome
	return e
}

// WithDetail attaches a non-sensitive fact.
func (e AuditEvent) WithDetail(key string, value any) AuditEvent {
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	e.Detail[key] = value
	return e
}
