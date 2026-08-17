package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
	"github.com/MuunBob/hoodalpha/internal/queue"
)

// WalletVerifyPayload is the body of a wallet:verify task.
type WalletVerifyPayload struct {
	WalletID    string    `json:"wallet_id"`
	RequestedAt time.Time `json:"requested_at"`
}

// WalletVerify confirms a wallet's address on the expected chain.
//
// Idempotent: the promotion to active only applies to a pending wallet, so a
// duplicate delivery re-reads the chain and changes nothing. Re-running it
// against a disabled wallet cannot resurrect it.
func WalletVerify(wallets *application.WalletService, log *slog.Logger) queue.Handler {
	log = log.With("task", queue.TypeWalletVerify)
	return func(ctx context.Context, payload []byte) error {
		var p WalletVerifyPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return queue.SkipRetry(fmt.Errorf("decode payload: %w", err))
		}
		if p.WalletID == "" {
			return queue.SkipRetry(errors.New("wallet_id is required"))
		}

		wallet, err := wallets.Verify(ctx, p.WalletID)
		switch {
		case err == nil:
			log.Info("wallet verified", "wallet_id", p.WalletID, "status", string(wallet.Status))
			return nil

		case errors.Is(err, postgres.ErrNotFound):
			// The wallet was deleted between enqueue and execution. Retrying
			// cannot make it exist.
			return queue.SkipRetry(fmt.Errorf("wallet %s not found", p.WalletID))

		case errors.Is(err, domain.ErrWrongChain):
			// The node is on the wrong network. Retrying the same task will
			// keep failing until an operator fixes the endpoint, so archive it
			// loudly instead of looping.
			log.Error("wallet verification hit the wrong chain", "wallet_id", p.WalletID, "error", err)
			return queue.SkipRetry(err)

		default:
			// Transient: RPC timeout, database blip. Let Asynq retry.
			return err
		}
	}
}

// NotificationPayload is the body of a telegram:notification task.
type NotificationPayload struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

// Notifier sends a message to a chat.
type Notifier interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// TelegramNotification delivers a queued message.
//
// Not idempotent in the strict sense — Telegram has no dedup key, so a retry
// after a partial failure can deliver twice. That is the right trade for
// notifications: a duplicate alert is noise, a missed one hides an incident.
// This handler must never be used for anything that moves money.
func TelegramNotification(notifier Notifier, log *slog.Logger) queue.Handler {
	log = log.With("task", queue.TypeTelegramNotification)
	return func(ctx context.Context, payload []byte) error {
		var p NotificationPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return queue.SkipRetry(fmt.Errorf("decode payload: %w", err))
		}
		if p.ChatID == 0 || p.Text == "" {
			return queue.SkipRetry(errors.New("chat_id and text are required"))
		}
		if notifier == nil {
			return errors.New("notifier is not configured")
		}

		if err := notifier.Send(ctx, p.ChatID, p.Text); err != nil {
			return fmt.Errorf("send notification: %w", err)
		}
		log.Debug("notification delivered", "chat_id", p.ChatID)
		return nil
	}
}
