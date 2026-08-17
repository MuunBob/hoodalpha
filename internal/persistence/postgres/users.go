package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// ErrNotFound reports a missing row.
var ErrNotFound = errors.New("not found")

// UserRepo persists accounts and their Telegram identities.
type UserRepo struct {
	pool *Pool
}

// NewUserRepo builds the repository.
func NewUserRepo(pool *Pool) *UserRepo { return &UserRepo{pool: pool} }

// EnsureTelegramUser returns the account for a Telegram ID, creating both the
// account and the link on first contact.
//
// It runs in one transaction so a crash midway cannot leave a telegram_users
// row pointing at no account. The whole operation is idempotent: calling it on
// every command is the intended usage.
func (r *UserRepo) EnsureTelegramUser(ctx context.Context, tu domain.TelegramUser) (domain.User, domain.TelegramUser, bool, error) {
	if !tu.TelegramID.Valid() {
		return domain.User{}, domain.TelegramUser{}, false, errors.New("invalid telegram id")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, domain.TelegramUser{}, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		userID  string
		created bool
	)
	err = tx.QueryRow(ctx,
		`SELECT user_id::text FROM telegram_users WHERE telegram_id = $1`,
		int64(tu.TelegramID)).Scan(&userID)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		created = true
		if err := tx.QueryRow(ctx,
			`INSERT INTO users (status) VALUES ('active') RETURNING id::text`).Scan(&userID); err != nil {
			return domain.User{}, domain.TelegramUser{}, false, fmt.Errorf("create user: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO telegram_users
			    (telegram_id, user_id, username, first_name, last_name, language_code, chat_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			int64(tu.TelegramID), userID, nullable(tu.Username), nullable(tu.FirstName),
			nullable(tu.LastName), nullable(tu.LanguageCode), tu.ChatID); err != nil {
			return domain.User{}, domain.TelegramUser{}, false, fmt.Errorf("link telegram user: %w", err)
		}
	case err != nil:
		return domain.User{}, domain.TelegramUser{}, false, fmt.Errorf("lookup telegram user: %w", err)
	default:
		// Refresh the cached display fields; they drift as users rename.
		if _, err := tx.Exec(ctx, `
			UPDATE telegram_users
			   SET username = $2, first_name = $3, last_name = $4,
			       language_code = $5, chat_id = $6, last_seen_at = now()
			 WHERE telegram_id = $1`,
			int64(tu.TelegramID), nullable(tu.Username), nullable(tu.FirstName),
			nullable(tu.LastName), nullable(tu.LanguageCode), tu.ChatID); err != nil {
			return domain.User{}, domain.TelegramUser{}, false, fmt.Errorf("refresh telegram user: %w", err)
		}
	}

	var user domain.User
	if err := tx.QueryRow(ctx,
		`SELECT id::text, status, created_at, updated_at FROM users WHERE id = $1`, userID).
		Scan(&user.ID, &user.Status, &user.CreatedAt, &user.UpdatedAt); err != nil {
		return domain.User{}, domain.TelegramUser{}, false, fmt.Errorf("load user: %w", err)
	}

	stored, err := scanTelegramUser(tx.QueryRow(ctx, telegramUserSelect+` WHERE telegram_id = $1`, int64(tu.TelegramID)))
	if err != nil {
		return domain.User{}, domain.TelegramUser{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, domain.TelegramUser{}, false, fmt.Errorf("commit: %w", err)
	}
	return user, stored, created, nil
}

const telegramUserSelect = `
SELECT telegram_id, user_id::text, coalesce(username, ''), coalesce(first_name, ''),
       coalesce(last_name, ''), coalesce(language_code, ''), chat_id,
       created_at, updated_at, last_seen_at
  FROM telegram_users`

// GetTelegramUser loads a Telegram identity.
func (r *UserRepo) GetTelegramUser(ctx context.Context, id domain.TelegramUserID) (domain.TelegramUser, error) {
	return scanTelegramUser(r.pool.QueryRow(ctx, telegramUserSelect+` WHERE telegram_id = $1`, int64(id)))
}

// GetUser loads an account.
func (r *UserRepo) GetUser(ctx context.Context, id domain.UserID) (domain.User, error) {
	var u domain.User
	err := r.pool.QueryRow(ctx,
		`SELECT id::text, status, created_at, updated_at FROM users WHERE id = $1`, id.String()).
		Scan(&u.ID, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTelegramUser(row rowScanner) (domain.TelegramUser, error) {
	var t domain.TelegramUser
	err := row.Scan(&t.TelegramID, &t.UserID, &t.Username, &t.FirstName, &t.LastName,
		&t.LanguageCode, &t.ChatID, &t.CreatedAt, &t.UpdatedAt, &t.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramUser{}, ErrNotFound
	}
	if err != nil {
		return domain.TelegramUser{}, fmt.Errorf("scan telegram user: %w", err)
	}
	return t, nil
}

// nullable maps an empty string to SQL NULL, so absent optional fields are
// stored as NULL rather than as an empty string that looks like data.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
