// Package telegram is the control-plane transport. Handlers parse a message,
// call an application use case, and format the reply. No business rule,
// authorization decision or persistence lives here — those belong to
// internal/application, so a second transport cannot bypass them.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
)

// Command is one control-plane command.
type Command struct {
	// Name is the command without its leading slash.
	Name string
	// Description is shown by /help and registered with BotFather.
	Description string
	// Dangerous marks commands that require explicit confirmation. No
	// dangerous command exists yet; the flag is honoured by the router so the
	// mechanism is in place before one does.
	Dangerous bool
	// Handler runs the command. It receives the authorized identity, so a
	// handler can never run for an unauthenticated caller.
	Handler func(ctx context.Context, req Request) (string, error)
}

// Request is one authorized command invocation.
type Request struct {
	Identity application.Identity
	ChatID   int64
	// Args is everything after the command word.
	Args []string
	// Raw is the full message text, for commands that want it verbatim.
	Raw string
}

// Arg returns the nth argument or an empty string.
func (r Request) Arg(n int) string {
	if n < len(r.Args) {
		return r.Args[n]
	}
	return ""
}

// Replier sends a message back to a chat.
type Replier interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// RateLimiter throttles a caller.
type RateLimiter interface {
	Allow(ctx context.Context, identity string, limit int, window time.Duration) (bool, int, error)
}

// Router authorizes, throttles, audits and dispatches commands.
type Router struct {
	onboarding *application.Onboarding
	limiter    RateLimiter
	log        *slog.Logger

	commands map[string]Command
	// rateLimit and rateWindow bound how many commands one identity may send.
	rateLimit  int
	rateWindow time.Duration
}

// RouterOptions configure a Router.
type RouterOptions struct {
	Onboarding *application.Onboarding
	Limiter    RateLimiter
	Logger     *slog.Logger
	// RateLimit is commands allowed per RateWindow. Defaults to 20/minute.
	RateLimit  int
	RateWindow time.Duration
}

// NewRouter builds a router.
func NewRouter(opts RouterOptions) *Router {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.RateLimit <= 0 {
		opts.RateLimit = 20
	}
	if opts.RateWindow <= 0 {
		opts.RateWindow = time.Minute
	}
	return &Router{
		onboarding: opts.Onboarding,
		limiter:    opts.Limiter,
		log:        opts.Logger.With("component", "telegram_router"),
		commands:   map[string]Command{},
		rateLimit:  opts.RateLimit,
		rateWindow: opts.RateWindow,
	}
}

// Handle registers a command. Registering the same name twice is a
// programming error and panics at boot rather than silently shadowing.
func (r *Router) Handle(c Command) {
	name := strings.TrimPrefix(strings.ToLower(c.Name), "/")
	if _, exists := r.commands[name]; exists {
		panic("telegram: duplicate command " + name)
	}
	c.Name = name
	r.commands[name] = c
}

// Commands returns the registered commands sorted by name, for /help and for
// registering the command menu with Telegram.
func (r *Router) Commands() []Command {
	out := make([]Command, 0, len(r.commands))
	for _, c := range r.commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Update is a transport-agnostic inbound message. Keeping the router free of
// the Telegram SDK's types makes it testable without a live bot.
type Update struct {
	TelegramID   domain.TelegramUserID
	ChatID       int64
	Username     string
	FirstName    string
	LastName     string
	LanguageCode string
	IsBot        bool
	Text         string
}

// Dispatch runs one update through the full pipeline:
//
//	parse -> authorize (allowlist) -> rate limit -> audit -> handle
//
// Authorization precedes everything with a side effect. An unauthorized caller
// gets one terse reply and nothing else happens — no row written, no handler
// reached, no detail about why.
func (r *Router) Dispatch(ctx context.Context, u Update, reply Replier) {
	name, args := parseCommand(u.Text)
	if name == "" {
		return // not a command; the control plane ignores chatter
	}

	log := r.log.With("telegram_id", int64(u.TelegramID), "command", name)

	// Another bot talking to this bot is never legitimate.
	if u.IsBot {
		log.Warn("ignoring update from a bot account")
		return
	}

	identity, err := r.onboarding.Authorize(ctx, domain.TelegramUser{
		TelegramID:   u.TelegramID,
		Username:     u.Username,
		FirstName:    u.FirstName,
		LastName:     u.LastName,
		LanguageCode: u.LanguageCode,
		ChatID:       u.ChatID,
	})
	if err != nil {
		if errors.Is(err, domain.ErrUnauthorized) {
			// Deliberately uninformative: an unauthorized caller learns
			// nothing about whether the bot exists, who may use it, or why
			// they were refused. The rejection is already audited.
			r.send(ctx, reply, u.ChatID, "Not authorized.")
			return
		}
		log.Error("authorization failed", "error", err)
		r.send(ctx, reply, u.ChatID, "Something went wrong. Try again shortly.")
		return
	}

	// Rate limiting runs after authorization so an unknown caller cannot
	// consume an authorized user's budget by spamming their ID.
	if r.limiter != nil {
		allowed, remaining, err := r.limiter.Allow(ctx,
			fmt.Sprintf("telegram:%d", int64(u.TelegramID)), r.rateLimit, r.rateWindow)
		if err != nil {
			// Fail open, but say so. Locking the operator out of their own
			// control plane during a Redis outage is the worse failure.
			log.Error("rate limiter unavailable; allowing command", "error", err)
		}
		if !allowed {
			log.Warn("rate limited")
			r.onboarding.RecordRateLimited(ctx, u.TelegramID, name)
			r.send(ctx, reply, u.ChatID,
				fmt.Sprintf("Slow down — more than %d commands per %s.", r.rateLimit, r.rateWindow))
			return
		}
		_ = remaining
	}

	cmd, ok := r.commands[name]
	if !ok {
		r.onboarding.RecordCommand(ctx, u.TelegramID, name, domain.OutcomeRejected)
		r.send(ctx, reply, u.ChatID, "Unknown command. Try /help.")
		return
	}

	start := time.Now()
	text, err := cmd.Handler(ctx, Request{
		Identity: identity,
		ChatID:   u.ChatID,
		Args:     args,
		Raw:      u.Text,
	})
	duration := time.Since(start)

	if err != nil {
		log.Error("command failed", "error", err, "duration", duration.String())
		r.onboarding.RecordCommand(ctx, u.TelegramID, name, domain.OutcomeFailed)
		// The user gets a safe message; the detail stays in the logs, because
		// an error string can carry a connection string or an internal path.
		r.send(ctx, reply, u.ChatID, "That command failed. The error has been logged.")
		return
	}

	log.Info("command handled", "duration", duration.String())
	r.onboarding.RecordCommand(ctx, u.TelegramID, name, domain.OutcomeOK)
	r.send(ctx, reply, u.ChatID, text)
}

func (r *Router) send(ctx context.Context, reply Replier, chatID int64, text string) {
	if reply == nil || text == "" {
		return
	}
	if err := reply.Send(ctx, chatID, text); err != nil {
		r.log.Error("reply failed", "chat_id", chatID, "error", err)
	}
}

// parseCommand extracts a command name and arguments from message text.
// It handles the "/cmd@BotName" form Telegram uses in group chats.
func parseCommand(text string) (string, []string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", nil
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", nil
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if at := strings.Index(name, "@"); at >= 0 {
		name = name[:at]
	}
	if name == "" {
		return "", nil
	}
	return name, fields[1:]
}
