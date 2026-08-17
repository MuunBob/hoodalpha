package queue

import (
	"context"
	"fmt"
	"time"
)

// Enqueuer adapts the Asynq client to the application's TaskEnqueuer port, so
// use cases describe work to schedule without importing Asynq.
type Enqueuer struct {
	client *Client
}

// NewEnqueuer wraps a client.
func NewEnqueuer(client *Client) *Enqueuer { return &Enqueuer{client: client} }

// EnqueueWalletVerify schedules on-chain verification of a wallet.
//
// The task ID is derived from the wallet ID, so linking the same wallet twice
// in quick succession schedules one verification rather than two. A duplicate
// is reported as success: the work is already queued.
func (e *Enqueuer) EnqueueWalletVerify(ctx context.Context, walletID string) error {
	payload := map[string]any{
		"wallet_id":    walletID,
		"requested_at": time.Now().UTC(),
	}
	_, err := e.client.Enqueue(ctx, TypeWalletVerify, payload, EnqueueOptions{
		Queue:  QueueCritical,
		TaskID: "wallet-verify:" + walletID,
		// Verification touches the chain, which can be briefly unavailable.
		MaxRetry:  5,
		Timeout:   30 * time.Second,
		Retention: time.Hour,
	})
	if err != nil && !IsDuplicate(err) {
		return fmt.Errorf("enqueue wallet verify: %w", err)
	}
	return nil
}

// EnqueueNotification schedules a Telegram message.
//
// No task ID: two identical alerts are usually two real events, and
// collapsing them would hide the second.
func (e *Enqueuer) EnqueueNotification(ctx context.Context, chatID int64, text string) error {
	payload := map[string]any{"chat_id": chatID, "text": text}
	_, err := e.client.Enqueue(ctx, TypeTelegramNotification, payload, EnqueueOptions{
		Queue:     QueueNotifications,
		MaxRetry:  3,
		Timeout:   20 * time.Second,
		Retention: 30 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("enqueue notification: %w", err)
	}
	return nil
}
