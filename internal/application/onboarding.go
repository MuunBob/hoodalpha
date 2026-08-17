package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// Onboarding turns a Telegram identity into an authorized account.
//
// This is the single place authorization is decided. Transport adapters
// (Telegram handlers, Mini App endpoints) call Authorize and act on the
// result; none of them re-implement the check, so there is one rule to audit.
type Onboarding struct {
	users     UserStore
	audit     AuditStore
	allowlist domain.Allowlist
	log       *slog.Logger
}

// NewOnboarding wires the use case.
func NewOnboarding(users UserStore, audit AuditStore, allowlist domain.Allowlist, log *slog.Logger) *Onboarding {
	if log == nil {
		log = slog.Default()
	}
	return &Onboarding{
		users:     users,
		audit:     audit,
		allowlist: allowlist,
		log:       log.With("component", "onboarding"),
	}
}

// Identity is an authorized caller.
type Identity struct {
	User     domain.User
	Telegram domain.TelegramUser
	// FirstContact reports whether the account was created by this call.
	FirstContact bool
}

// Authorize checks the allowlist and returns the caller's account, creating it
// on first contact.
//
// Order matters: the allowlist is checked before any row is written, so an
// unauthorized user cannot populate the database by sending messages.
// Every rejection is audited — a burst of them is what an attack looks like.
func (o *Onboarding) Authorize(ctx context.Context, tu domain.TelegramUser) (Identity, error) {
	if !tu.TelegramID.Valid() {
		return Identity{}, domain.ErrUnauthorized
	}

	if !o.allowlist.Allows(tu.TelegramID) {
		o.log.Warn("rejected unauthorized telegram user",
			"telegram_id", int64(tu.TelegramID))
		o.recordAudit(ctx, domain.NewAuditEvent(
			domain.ActorTelegramUser, telegramActorID(tu.TelegramID), domain.ActionTelegramUnauthorized).
			WithOutcome(domain.OutcomeRejected).
			WithDetail("reason", "not in allowlist"))
		return Identity{}, domain.ErrUnauthorized
	}

	user, stored, created, err := o.users.EnsureTelegramUser(ctx, tu)
	if err != nil {
		return Identity{}, fmt.Errorf("ensure telegram user: %w", err)
	}

	// A suspended account passes the allowlist but must still be refused, so
	// an operator can revoke access without editing configuration and
	// restarting the process.
	if user.Status != domain.UserActive {
		o.recordAudit(ctx, domain.NewAuditEvent(
			domain.ActorTelegramUser, telegramActorID(tu.TelegramID), domain.ActionTelegramUnauthorized).
			WithSubject("user", user.ID.String()).
			WithOutcome(domain.OutcomeRejected).
			WithDetail("reason", "account "+string(user.Status)))
		return Identity{}, domain.ErrUnauthorized
	}

	if created {
		o.log.Info("onboarded telegram user", "telegram_id", int64(tu.TelegramID), "user_id", user.ID.String())
		o.recordAudit(ctx, domain.NewAuditEvent(
			domain.ActorTelegramUser, telegramActorID(tu.TelegramID), domain.ActionUserOnboarded).
			WithSubject("user", user.ID.String()))
	}

	return Identity{User: user, Telegram: stored, FirstContact: created}, nil
}

// RecordCommand audits a command invocation. Every command is logged, so an
// unexpected action can be traced to an identity and a time.
func (o *Onboarding) RecordCommand(ctx context.Context, id domain.TelegramUserID, command string, outcome string) {
	o.recordAudit(ctx, domain.NewAuditEvent(
		domain.ActorTelegramUser, telegramActorID(id), domain.ActionTelegramCommand).
		WithOutcome(outcome).
		WithDetail("command", command))
}

// RecordRateLimited audits a throttled caller.
func (o *Onboarding) RecordRateLimited(ctx context.Context, id domain.TelegramUserID, command string) {
	o.recordAudit(ctx, domain.NewAuditEvent(
		domain.ActorTelegramUser, telegramActorID(id), domain.ActionTelegramRateLimited).
		WithOutcome(domain.OutcomeRejected).
		WithDetail("command", command))
}

// recordAudit never fails the caller. A trading command must not be refused
// because the audit write failed, but the failure itself is logged loudly:
// a silently empty audit trail is worse than a noisy one.
func (o *Onboarding) recordAudit(ctx context.Context, e domain.AuditEvent) {
	if o.audit == nil {
		return
	}
	if err := o.audit.Record(ctx, e); err != nil {
		o.log.Error("audit write failed", "action", e.Action, "error", err)
	}
}

func telegramActorID(id domain.TelegramUserID) string {
	return fmt.Sprintf("telegram:%d", int64(id))
}

// ErrNoAllowlist reports that no identity is configured, so the control plane
// would refuse everyone.
var ErrNoAllowlist = errors.New("telegram allowlist is empty; no user can control the bot")

// AllowlistEmpty reports whether the allowlist admits nobody.
func (o *Onboarding) AllowlistEmpty() bool { return o.allowlist.Empty() }
