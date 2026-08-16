-- +goose Up
-- +goose StatementBegin

-- chain_sync_state is the restart anchor: it records how far the bot has
-- observed each chain. Current-state table, one row per chain, no partitioning.
CREATE TABLE chain_sync_state (
    chain_id            BIGINT      PRIMARY KEY,
    chain_name          TEXT        NOT NULL,
    last_block_number   BIGINT      NOT NULL,
    last_block_hash     TEXT        NOT NULL,
    last_block_time     TIMESTAMPTZ NOT NULL,
    observed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT chain_sync_state_chain_id_positive
        CHECK (chain_id > 0),
    CONSTRAINT chain_sync_state_block_number_nonneg
        CHECK (last_block_number >= 0),
    -- Hashes are stored lowercase so lookups never need case folding.
    CONSTRAINT chain_sync_state_block_hash_format
        CHECK (last_block_hash ~ '^0x[0-9a-f]{64}$')
);

COMMENT ON TABLE chain_sync_state IS
    'Highest block observed per chain. Read on startup before any trading resumes.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS chain_sync_state;
-- +goose StatementEnd
