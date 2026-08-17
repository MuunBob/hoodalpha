package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	tgbot "github.com/go-telegram/bot"
)

// Notifier sends outbound messages without running the command loop.
//
// The worker needs to deliver alerts but must not also poll for updates: two
// processes long-polling the same bot token would compete for updates and each
// would see a random half of them.
type Notifier struct {
	api *tgbot.Bot
	log *slog.Logger
}

// NewNotifier builds a send-only Telegram client.
func NewNotifier(token string, log *slog.Logger) (*Notifier, error) {
	if token == "" {
		return nil, errors.New("telegram bot token is required")
	}
	if log == nil {
		log = slog.Default()
	}
	// No handler is registered and Start is never called, so this client only
	// ever makes outbound API calls.
	api, err := tgbot.New(token)
	if err != nil {
		return nil, fmt.Errorf("create telegram notifier: %w", err)
	}
	return &Notifier{api: api, log: log.With("component", "telegram_notifier")}, nil
}

// Send delivers a plain-text message.
func (n *Notifier) Send(ctx context.Context, chatID int64, text string) error {
	if text == "" {
		return nil
	}
	const maxLen = 4000
	if len(text) > maxLen {
		text = text[:maxLen] + "\n… truncated"
	}
	_, err := n.api.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: chatID, Text: text})
	if err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}
	return nil
}
