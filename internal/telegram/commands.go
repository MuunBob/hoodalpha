package telegram

import (
	"context"
	"fmt"
	"strings"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
)

// CommandDeps are the use cases the handlers call. Handlers hold no store,
// no chain client and no database handle of their own.
type CommandDeps struct {
	Health  *application.HealthChecker
	Wallets *application.WalletService
	ChainID uint64
	// MiniAppURL is where /connect sends the user. Empty when unconfigured.
	MiniAppURL string
	Version    string
}

// Register wires the Phase 2 command set onto a router.
func Register(r *Router, d CommandDeps) {
	r.Handle(Command{
		Name:        "start",
		Description: "Register and show what this bot can do",
		Handler:     handleStart(d),
	})
	r.Handle(Command{
		Name:        "status",
		Description: "Bot state, linked wallets and trading status",
		Handler:     handleStatus(d),
	})
	r.Handle(Command{
		Name:        "health",
		Description: "Dependency health (database, cache, chain)",
		Handler:     handleHealth(d),
	})
	r.Handle(Command{
		Name:        "connect",
		Description: "Link a wallet",
		Handler:     handleConnect(d),
	})
	r.Handle(Command{
		Name:        "help",
		Description: "List available commands",
		Handler:     handleHelp(r),
	})
}

func handleStart(d CommandDeps) func(context.Context, Request) (string, error) {
	return func(ctx context.Context, req Request) (string, error) {
		var b strings.Builder
		if req.Identity.FirstContact {
			fmt.Fprintf(&b, "Welcome, %s. Your account is registered.\n\n",
				req.Identity.Telegram.DisplayName())
		} else {
			fmt.Fprintf(&b, "Welcome back, %s.\n\n", req.Identity.Telegram.DisplayName())
		}

		fmt.Fprintf(&b, "Network: %s (chain %d)\n\n", domain.ChainName(d.ChainID), d.ChainID)
		b.WriteString("Available now:\n")
		b.WriteString("  /connect — link a wallet\n")
		b.WriteString("  /status  — wallets and trading status\n")
		b.WriteString("  /health  — dependency health\n")
		b.WriteString("  /help    — list commands\n\n")
		// Stating this plainly matters more than it looks: a user who believes
		// the bot is trading when it is not will misread every later message.
		b.WriteString("This build can read the chain and manage wallet records. " +
			"It cannot trade: there is no signer and no execution engine yet.")
		return b.String(), nil
	}
}

func handleStatus(d CommandDeps) func(context.Context, Request) (string, error) {
	return func(ctx context.Context, req Request) (string, error) {
		var b strings.Builder
		fmt.Fprintf(&b, "Account: %s\n", req.Identity.User.ID)
		fmt.Fprintf(&b, "Network: %s (chain %d)\n", domain.ChainName(d.ChainID), d.ChainID)
		b.WriteString("Trading: disabled (not implemented in this phase)\n\n")

		if d.Wallets == nil {
			b.WriteString("Wallets: unavailable")
			return b.String(), nil
		}

		views, err := d.Wallets.List(ctx, req.Identity.User.ID)
		if err != nil {
			return "", fmt.Errorf("list wallets: %w", err)
		}
		if len(views) == 0 {
			b.WriteString("No wallets linked. Use /connect to add one.")
			return b.String(), nil
		}

		fmt.Fprintf(&b, "Wallets (%d):\n", len(views))
		for _, v := range views {
			fmt.Fprintf(&b, "\n  %s\n", v.Wallet.Address)
			fmt.Fprintf(&b, "  role: %s   status: %s\n", v.Wallet.Role, v.Wallet.Status)
			if v.BalanceError != "" {
				b.WriteString("  balance: unavailable\n")
			} else {
				fmt.Fprintf(&b, "  balance: %s ETH\n", v.Balance.Ether())
			}
			if v.Policy.WalletID != "" {
				fmt.Fprintf(&b, "  limits: max %.1f%%/position, %d open, %.1f%% daily loss\n",
					v.Policy.MaxPositionPercent, v.Policy.MaxOpenPositions,
					v.Policy.DailyLossLimitPercent)
				fmt.Fprintf(&b, "  trading enabled: %t\n", v.Policy.TradingEnabled)
			}
		}
		return b.String(), nil
	}
}

func handleHealth(d CommandDeps) func(context.Context, Request) (string, error) {
	return func(ctx context.Context, req Request) (string, error) {
		if d.Health == nil {
			return "Health checks are not configured.", nil
		}
		report := d.Health.Check(ctx)

		var b strings.Builder
		fmt.Fprintf(&b, "Status: %s\n", strings.ToUpper(string(report.Status)))
		fmt.Fprintf(&b, "Version: %s\n\n", report.Version)
		for _, c := range report.Components {
			fmt.Fprintf(&b, "  %-10s %s", c.Name, c.Status)
			if c.LatencyMS > 0 {
				fmt.Fprintf(&b, " (%dms)", c.LatencyMS)
			}
			if c.Error != "" {
				fmt.Fprintf(&b, " — %s", c.Error)
			}
			b.WriteByte('\n')
			if block, ok := c.Details["last_block"]; ok {
				fmt.Fprintf(&b, "             block %s\n", block)
			}
		}
		return b.String(), nil
	}
}

func handleConnect(d CommandDeps) func(context.Context, Request) (string, error) {
	return func(ctx context.Context, req Request) (string, error) {
		address := strings.TrimSpace(req.Arg(0))

		// With no address, point the user at the Mini App. Wallet
		// authorization belongs in a proper UI, not in chat messages — and a
		// chat message is exactly where a user might paste a seed phrase.
		if address == "" {
			var b strings.Builder
			b.WriteString("Link a wallet:\n\n")
			if d.MiniAppURL != "" {
				fmt.Fprintf(&b, "  Open the Mini App: %s\n\n", d.MiniAppURL)
			}
			b.WriteString("  Or send: /connect 0xYourAddress\n\n")
			b.WriteString("Only a public address is ever needed. " +
				"Never send a private key or seed phrase — not to this bot, not to anyone.")
			return b.String(), nil
		}

		// Reject anything that looks like key material before it is parsed,
		// logged, or stored. A 64-hex string or a word list pasted here is a
		// user mistake that must fail loudly and immediately.
		if looksLikeSecret(address) {
			return "That looks like a private key or seed phrase — never share one.\n\n" +
				"It was not stored. Rotate that wallet immediately if it is real, " +
				"then send only your public address: /connect 0xYourAddress", nil
		}

		if d.Wallets == nil {
			return "", fmt.Errorf("wallet service is not configured")
		}

		wallet, err := d.Wallets.Link(ctx, application.LinkRequest{
			UserID:  req.Identity.User.ID,
			Address: address,
			Role:    domain.WalletRoleOwner,
			ActorID: fmt.Sprintf("telegram:%d", int64(req.Identity.Telegram.TelegramID)),
		})
		switch {
		case err == nil:
		case strings.Contains(err.Error(), "invalid evm address"):
			return "That is not a valid EVM address. It should look like 0x followed by 40 hex characters.", nil
		case err == domain.ErrWalletAlreadyLinked:
			return "That wallet is already linked to your account. Use /status to see it.", nil
		default:
			return "", err
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Wallet linked: %s\n\n", wallet.Address)
		fmt.Fprintf(&b, "Status: %s — verifying it on %s.\n",
			wallet.Status, domain.ChainName(wallet.ChainID))
		b.WriteString("It becomes active once the address is confirmed on-chain. " +
			"Check /status in a moment.\n\n")
		b.WriteString("Trading stays disabled until you enable it explicitly.")
		return b.String(), nil
	}
}

func handleHelp(r *Router) func(context.Context, Request) (string, error) {
	return func(ctx context.Context, req Request) (string, error) {
		var b strings.Builder
		b.WriteString("Commands:\n\n")
		for _, c := range r.Commands() {
			fmt.Fprintf(&b, "  /%-9s %s\n", c.Name, c.Description)
		}
		return b.String(), nil
	}
}

// looksLikeSecret catches the two shapes a user is most likely to paste by
// mistake: a raw private key, and a BIP-39 mnemonic. It is a guard against
// accidents, not against a determined user — but an accident here is
// unrecoverable, so it is worth catching cheaply.
func looksLikeSecret(s string) bool {
	trimmed := strings.TrimSpace(s)

	// A 64-hex-character string, with or without 0x, is private-key shaped.
	hex := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(hex) == 64 && isHex(hex) {
		return true
	}

	// BIP-39 mnemonics are 12, 15, 18, 21 or 24 words.
	words := strings.Fields(trimmed)
	switch len(words) {
	case 12, 15, 18, 21, 24:
		for _, w := range words {
			if !isAlpha(w) {
				return false
			}
		}
		return true
	}
	return false
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func isAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}
