package application

import (
	"context"
	"time"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// Ports the use cases depend on. They are declared here, in the layer that
// consumes them, so infrastructure implements the application's needs rather
// than the application shaping itself around a driver.

// UserStore persists accounts and Telegram identities.
type UserStore interface {
	// EnsureTelegramUser returns the account for a Telegram identity, creating
	// it on first contact. The bool reports whether it was created now.
	EnsureTelegramUser(ctx context.Context, tu domain.TelegramUser) (domain.User, domain.TelegramUser, bool, error)
	GetTelegramUser(ctx context.Context, id domain.TelegramUserID) (domain.TelegramUser, error)
	GetUser(ctx context.Context, id domain.UserID) (domain.User, error)
}

// WalletStore persists wallets and policies. It never persists key material.
type WalletStore interface {
	Link(ctx context.Context, w domain.Wallet, p domain.WalletPolicy) (domain.Wallet, error)
	Get(ctx context.Context, id string) (domain.Wallet, error)
	ListByUser(ctx context.Context, userID domain.UserID) ([]domain.Wallet, error)
	MarkVerified(ctx context.Context, id string, at time.Time) error
	SetStatus(ctx context.Context, id string, next domain.WalletStatus) error
	GetPolicy(ctx context.Context, walletID string) (domain.WalletPolicy, error)
	UpdatePolicy(ctx context.Context, walletID string, p domain.WalletPolicy) error
}

// AuditStore appends security-relevant events.
type AuditStore interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}

// TaskEnqueuer schedules background work. The use cases describe what should
// happen; the queue adapter decides how it is delivered.
type TaskEnqueuer interface {
	// EnqueueWalletVerify schedules on-chain verification of a wallet.
	EnqueueWalletVerify(ctx context.Context, walletID string) error
	// EnqueueNotification schedules a message to a Telegram chat.
	EnqueueNotification(ctx context.Context, chatID int64, text string) error
}

// AddressProbe reads chain state for an address. Implemented by the chain
// client; declared narrowly so the use case cannot reach for a signer that
// does not exist.
type AddressProbe interface {
	ChainID(ctx context.Context) (uint64, error)
	BalanceAt(ctx context.Context, addr domain.Address, blockNumber *uint64) (domain.Wei, error)
}
