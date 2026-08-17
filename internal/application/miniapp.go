package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/telegram/initdata"
)

// InitDataVerifier verifies signed Mini App payloads.
type InitDataVerifier interface {
	Verify(raw string) (initdata.Data, error)
}

// MiniAppAuth authenticates Mini App requests.
//
// The Mini App is an untrusted UI client: it runs on the user's device and
// anything it sends can be forged. The only thing carrying proof is the signed
// initData string, so authentication starts from a signature check and derives
// the identity from the verified payload — never from a client-supplied user
// ID, and never from initDataUnsafe.
type MiniAppAuth struct {
	verifier   InitDataVerifier
	onboarding *Onboarding
	audit      AuditStore
	log        *slog.Logger
}

// NewMiniAppAuth wires the use case.
func NewMiniAppAuth(verifier InitDataVerifier, onboarding *Onboarding, audit AuditStore, log *slog.Logger) *MiniAppAuth {
	if log == nil {
		log = slog.Default()
	}
	return &MiniAppAuth{
		verifier:   verifier,
		onboarding: onboarding,
		audit:      audit,
		log:        log.With("component", "miniapp_auth"),
	}
}

// Authenticate verifies raw initData and resolves it to an authorized account.
//
// Two independent gates must both pass: the payload must be signed by this
// bot's token (proving Telegram produced it), and the identity inside it must
// be on the allowlist (proving we accept that person). A valid signature from
// a stranger is still refused.
func (m *MiniAppAuth) Authenticate(ctx context.Context, rawInitData string) (Identity, error) {
	if m.verifier == nil {
		return Identity{}, errors.New("mini app verification is not configured")
	}

	data, err := m.verifier.Verify(rawInitData)
	if err != nil {
		// The reason is recorded, the payload never is: it carries user data
		// and, in the replay case, a still-valid signature.
		m.log.Warn("mini app init data rejected", "reason", err.Error())
		m.recordAudit(ctx, domain.NewAuditEvent(domain.ActorSystem, "", domain.ActionMiniAppAuthRejected).
			WithOutcome(domain.OutcomeRejected).
			WithDetail("reason", err.Error()))
		return Identity{}, fmt.Errorf("%w: %v", domain.ErrUnauthorized, err)
	}

	// A bot account should never be driving the Mini App.
	if data.User.IsBot {
		m.recordAudit(ctx, domain.NewAuditEvent(
			domain.ActorTelegramUser, telegramActorID(domain.TelegramUserID(data.User.ID)),
			domain.ActionMiniAppAuthRejected).
			WithOutcome(domain.OutcomeRejected).
			WithDetail("reason", "bot accounts may not authenticate"))
		return Identity{}, domain.ErrUnauthorized
	}

	// Identity comes from the verified payload, so the allowlist check runs
	// against a signed user ID rather than a claimed one.
	identity, err := m.onboarding.Authorize(ctx, domain.TelegramUser{
		TelegramID:   domain.TelegramUserID(data.User.ID),
		Username:     data.User.Username,
		FirstName:    data.User.FirstName,
		LastName:     data.User.LastName,
		LanguageCode: data.User.LanguageCode,
		ChatID:       data.User.ID,
	})
	if err != nil {
		return Identity{}, err
	}

	m.recordAudit(ctx, domain.NewAuditEvent(
		domain.ActorTelegramUser, telegramActorID(identity.Telegram.TelegramID), domain.ActionMiniAppAuth).
		WithSubject("user", identity.User.ID.String()))

	return identity, nil
}

func (m *MiniAppAuth) recordAudit(ctx context.Context, e domain.AuditEvent) {
	if m.audit == nil {
		return
	}
	if err := m.audit.Record(ctx, e); err != nil {
		m.log.Error("audit write failed", "action", e.Action, "error", err)
	}
}
