package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrUnauthorized is returned when an identity is not permitted to act.
var ErrUnauthorized = errors.New("unauthorized")

// UserID is the internal identifier for an account. It is deliberately
// distinct from TelegramUserID: Telegram is one way to reach a user, not the
// user's identity, and a later phase may add another channel.
type UserID string

// String returns the identifier.
func (u UserID) String() string { return string(u) }

// TelegramUserID is Telegram's numeric user ID. It is the primary identity
// mechanism for the control plane — usernames are mutable and reusable, so
// authorizing on them would let a released username inherit access.
type TelegramUserID int64

// Valid reports whether the ID could have come from Telegram.
func (t TelegramUserID) Valid() bool { return t > 0 }

// User is an account the bot acts on behalf of.
type User struct {
	ID        UserID
	Status    UserStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UserStatus controls whether an account may act at all.
type UserStatus string

const (
	// UserActive can use the bot.
	UserActive UserStatus = "active"
	// UserSuspended is retained but blocked. Records are never deleted
	// outright, because they are referenced by audit history.
	UserSuspended UserStatus = "suspended"
)

// Valid reports whether the status is a known value.
func (s UserStatus) Valid() bool {
	return s == UserActive || s == UserSuspended
}

// TelegramUser links a Telegram identity to an account.
type TelegramUser struct {
	TelegramID TelegramUserID
	UserID     UserID
	// Username and display fields are cached for operator-facing output only.
	// Nothing authorizes on them.
	Username     string
	FirstName    string
	LastName     string
	LanguageCode string
	// ChatID is where the bot sends notifications for this user.
	ChatID     int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastSeenAt time.Time
}

// DisplayName returns a human-readable label for logs and messages.
func (t TelegramUser) DisplayName() string {
	if t.Username != "" {
		return "@" + t.Username
	}
	name := strings.TrimSpace(t.FirstName + " " + t.LastName)
	if name != "" {
		return name
	}
	return fmt.Sprintf("user %d", t.TelegramID)
}

// Allowlist is the set of Telegram user IDs permitted to control the bot.
//
// It is a closed allowlist rather than an open bot: this process can move real
// funds in later phases, so an unknown user must be refused by default rather
// than reaching any handler.
type Allowlist struct {
	ids map[TelegramUserID]struct{}
}

// NewAllowlist builds an allowlist from configured IDs.
func NewAllowlist(ids []TelegramUserID) Allowlist {
	set := make(map[TelegramUserID]struct{}, len(ids))
	for _, id := range ids {
		if id.Valid() {
			set[id] = struct{}{}
		}
	}
	return Allowlist{ids: set}
}

// Allows reports whether the Telegram user may control the bot.
// An empty allowlist allows nobody — an unconfigured deployment must be
// closed, not open to the world.
func (a Allowlist) Allows(id TelegramUserID) bool {
	if len(a.ids) == 0 {
		return false
	}
	_, ok := a.ids[id]
	return ok
}

// Empty reports whether no user is configured, so startup can warn loudly.
func (a Allowlist) Empty() bool { return len(a.ids) == 0 }

// Size returns how many identities are permitted.
func (a Allowlist) Size() int { return len(a.ids) }
