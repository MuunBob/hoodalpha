package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// Bot connects the router to Telegram's Bot API.
//
// It is the only file that imports the Telegram SDK. Everything above it works
// on the transport-agnostic Update type, so the control plane can be tested
// without a live bot and a second transport would not duplicate the rules.
type Bot struct {
	api    *tgbot.Bot
	router *Router
	log    *slog.Logger
}

// BotOptions configure a Bot.
type BotOptions struct {
	Token  string
	Router *Router
	Logger *slog.Logger
}

// NewBot builds the Telegram transport.
func NewBot(opts BotOptions) (*Bot, error) {
	if opts.Token == "" {
		return nil, errors.New("telegram bot token is required")
	}
	if opts.Router == nil {
		return nil, errors.New("router is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	log := opts.Logger.With("component", "telegram_bot")

	b := &Bot{router: opts.Router, log: log}

	api, err := tgbot.New(opts.Token,
		tgbot.WithDefaultHandler(b.handleUpdate),
		// The SDK would otherwise log at its own discretion; route it through
		// the project logger so nothing bypasses structured logging.
		tgbot.WithErrorsHandler(func(err error) {
			log.Error("telegram transport error", "error", err)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}
	b.api = api
	return b, nil
}

// Run starts long polling and blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	// Publish the command menu so users see the available commands. A failure
	// here is cosmetic — the commands still work — so it does not stop startup.
	if err := b.registerCommands(ctx); err != nil {
		b.log.Warn("could not register command menu", "error", err)
	}

	me, err := b.api.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram getMe: %w", err)
	}
	b.log.Info("telegram bot connected", "username", me.Username, "bot_id", me.ID)

	b.api.Start(ctx) // returns when ctx is cancelled
	b.log.Info("telegram bot stopped")
	return nil
}

func (b *Bot) registerCommands(ctx context.Context) error {
	cmds := b.router.Commands()
	out := make([]models.BotCommand, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, models.BotCommand{Command: c.Name, Description: c.Description})
	}
	_, err := b.api.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{Commands: out})
	return err
}

// handleUpdate converts an SDK update into the transport-agnostic form and
// hands it to the router. It deliberately does no authorization of its own.
func (b *Bot) handleUpdate(ctx context.Context, api *tgbot.Bot, update *models.Update) {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return
	}
	msg := update.Message

	b.router.Dispatch(ctx, Update{
		TelegramID:   domain.TelegramUserID(msg.From.ID),
		ChatID:       msg.Chat.ID,
		Username:     msg.From.Username,
		FirstName:    msg.From.FirstName,
		LastName:     msg.From.LastName,
		LanguageCode: msg.From.LanguageCode,
		IsBot:        msg.From.IsBot,
		Text:         msg.Text,
	}, b)
}

// Send delivers a plain-text reply. Messages are sent without parse mode:
// addresses and balances are rendered verbatim, and no user-supplied text can
// break formatting or inject markup.
func (b *Bot) Send(ctx context.Context, chatID int64, text string) error {
	if text == "" {
		return nil
	}
	// Telegram rejects messages over 4096 characters.
	const maxLen = 4000
	if len(text) > maxLen {
		text = text[:maxLen] + "\n… truncated"
	}
	_, err := b.api.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	if err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}
	return nil
}
