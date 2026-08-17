-- +goose Up
-- +goose StatementBegin

-- Account identity. Deliberately separate from telegram_users: Telegram is one
-- way to reach a user, not the user's identity, and a later phase may add
-- another channel without reshaping ownership of wallets and policies.
--
-- Not partitioned. This is bounded current-state data (a personal bot has a
-- handful of users); partitioning would add child-table management and
-- cross-partition joins for no gain.
CREATE TABLE users (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    status     TEXT        NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT users_status_valid CHECK (status IN ('active', 'suspended'))
);

COMMENT ON TABLE users IS
    'Accounts the bot acts on behalf of. Never deleted: audit history references them.';

-- +goose StatementEnd

-- +goose StatementBegin

-- Telegram identity, keyed by Telegram's numeric user ID.
--
-- telegram_id is the PRIMARY KEY because it is the authorization subject. The
-- username is cached for operator-facing output only and is explicitly NOT
-- unique: Telegram usernames are mutable and reusable, so treating one as an
-- identity would let a released username inherit another user's access.
CREATE TABLE telegram_users (
    telegram_id   BIGINT      PRIMARY KEY,
    user_id       UUID        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    username      TEXT,
    first_name    TEXT,
    last_name     TEXT,
    language_code TEXT,
    -- Where notifications go. Usually equal to telegram_id for private chats,
    -- but stored separately because they are not the same concept.
    chat_id      BIGINT      NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT telegram_users_id_positive CHECK (telegram_id > 0)
);

-- One Telegram account maps to exactly one internal account.
CREATE UNIQUE INDEX telegram_users_user_id_key ON telegram_users (user_id);

COMMENT ON COLUMN telegram_users.username IS
    'Cached for display only. Never used for authorization: usernames are mutable and reusable.';

-- +goose StatementEnd

-- +goose StatementBegin

-- On-chain addresses associated with an account.
--
-- This table holds NO private key, seed phrase or other signing material, and
-- no column exists for one. Signing material belongs to a hardened signer
-- introduced in a later phase; the schema makes storing a secret here
-- impossible rather than merely discouraged.
CREATE TABLE wallets (
    id      UUID   PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID   NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    -- Stored lowercase so lookups never depend on checksum casing.
    address  TEXT   NOT NULL,
    chain_id BIGINT NOT NULL,
    role     TEXT   NOT NULL,
    status   TEXT   NOT NULL DEFAULT 'pending',
    label    TEXT,
    -- NULL until the address has been confirmed on the expected chain.
    verified_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT wallets_address_format CHECK (address ~ '^0x[0-9a-f]{40}$'),
    CONSTRAINT wallets_address_not_zero
        CHECK (address <> '0x0000000000000000000000000000000000000000'),
    CONSTRAINT wallets_chain_id_positive CHECK (chain_id > 0),
    CONSTRAINT wallets_role_valid   CHECK (role IN ('owner', 'bot')),
    CONSTRAINT wallets_status_valid CHECK (status IN ('pending', 'active', 'disabled')),
    -- A wallet is only verified in a state that implies verification.
    CONSTRAINT wallets_verified_implies_not_pending
        CHECK (verified_at IS NULL OR status <> 'pending')
);

-- The same address on the same chain must not be linked twice to one account.
CREATE UNIQUE INDEX wallets_user_chain_address_key
    ON wallets (user_id, chain_id, address);

-- Lookup by address during verification and, later, transaction attribution.
CREATE INDEX wallets_chain_address_idx ON wallets (chain_id, address);
CREATE INDEX wallets_user_status_idx   ON wallets (user_id, status);

-- At most one active bot wallet per account per chain. Two concurrently active
-- signing wallets would make position and balance accounting ambiguous.
CREATE UNIQUE INDEX wallets_one_active_bot_per_chain
    ON wallets (user_id, chain_id)
    WHERE role = 'bot' AND status = 'active';

COMMENT ON TABLE wallets IS
    'Addresses only. No private keys, seed phrases or signing material are stored here or anywhere else.';

-- +goose StatementEnd

-- +goose StatementBegin

-- Per-wallet spending and risk envelope, enforced server-side.
--
-- NUMERIC, never floating point: these values bound how much real money can
-- move, and binary floating point cannot represent 0.1 exactly.
CREATE TABLE wallet_policies (
    id        UUID NOT NULL DEFAULT gen_random_uuid(),
    wallet_id UUID NOT NULL REFERENCES wallets (id) ON DELETE CASCADE,

    max_position_percent     NUMERIC(6, 3) NOT NULL,
    max_open_positions       INTEGER       NOT NULL,
    daily_loss_limit_percent NUMERIC(6, 3) NOT NULL,
    stop_loss_percent        NUMERIC(6, 3) NOT NULL,
    capital_recovery_percent NUMERIC(8, 3) NOT NULL,
    max_slippage_bps         INTEGER       NOT NULL,
    min_liquidity_usd        NUMERIC(20, 2) NOT NULL,

    -- Defaults to false: linking a wallet must never by itself authorise
    -- trading. Enabling it is a separate, audited decision.
    trading_enabled BOOLEAN     NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (id),

    CONSTRAINT wallet_policies_max_position_range
        CHECK (max_position_percent > 0 AND max_position_percent <= 100),
    CONSTRAINT wallet_policies_open_positions_positive
        CHECK (max_open_positions >= 1),
    CONSTRAINT wallet_policies_daily_loss_range
        CHECK (daily_loss_limit_percent > 0 AND daily_loss_limit_percent <= 100),
    CONSTRAINT wallet_policies_stop_loss_range
        CHECK (stop_loss_percent > 0 AND stop_loss_percent < 100),
    CONSTRAINT wallet_policies_capital_recovery_positive
        CHECK (capital_recovery_percent > 0),
    CONSTRAINT wallet_policies_slippage_range
        CHECK (max_slippage_bps >= 0 AND max_slippage_bps <= 10000),
    CONSTRAINT wallet_policies_min_liquidity_nonneg
        CHECK (min_liquidity_usd >= 0)
);

-- Exactly one policy per wallet. A wallet with two policies has no policy.
CREATE UNIQUE INDEX wallet_policies_wallet_id_key ON wallet_policies (wallet_id);

COMMENT ON TABLE wallet_policies IS
    'Hard limits enforced by the backend. Telegram and the Mini App may request changes; neither enforces them.';

-- +goose StatementEnd

-- +goose StatementBegin
-- Keep updated_at honest without every caller remembering to set it.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER telegram_users_set_updated_at
    BEFORE UPDATE ON telegram_users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER wallets_set_updated_at
    BEFORE UPDATE ON wallets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER wallet_policies_set_updated_at
    BEFORE UPDATE ON wallet_policies
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS wallet_policies;
DROP TABLE IF EXISTS wallets;
DROP TABLE IF EXISTS telegram_users;
DROP TABLE IF EXISTS users;
DROP FUNCTION IF EXISTS set_updated_at();
-- +goose StatementEnd
