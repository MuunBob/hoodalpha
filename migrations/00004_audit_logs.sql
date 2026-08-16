-- +goose Up
-- +goose StatementBegin

-- audit_logs is append-only and high-volume: every command, state transition
-- and operator action lands here. It is the one foundation table whose growth
-- justifies partitioning, so it is RANGE-partitioned by time and handed to
-- pg_partman for child creation and retention.
--
-- Tables deliberately NOT partitioned: users, telegram_users, wallets,
-- wallet_policies, tokens, token_contracts, orders, positions,
-- chain_sync_state. They are bounded current-state tables; partitioning them
-- would add child-table management cost and cross-partition joins for no gain.
CREATE TABLE timeseries.audit_logs (
    id            UUID        NOT NULL DEFAULT gen_random_uuid(),
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_type    TEXT        NOT NULL,
    actor_id      TEXT,
    action        TEXT        NOT NULL,
    subject_type  TEXT,
    subject_id    TEXT,
    outcome       TEXT        NOT NULL DEFAULT 'ok',
    detail        JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- The partition key must be part of every unique constraint in PostgreSQL,
    -- hence the composite primary key rather than id alone.
    PRIMARY KEY (id, occurred_at),

    CONSTRAINT audit_logs_actor_type_valid
        CHECK (actor_type IN ('system', 'telegram_user', 'worker', 'operator')),
    CONSTRAINT audit_logs_outcome_valid
        CHECK (outcome IN ('ok', 'rejected', 'failed'))
) PARTITION BY RANGE (occurred_at);

CREATE INDEX audit_logs_occurred_at_idx
    ON timeseries.audit_logs (occurred_at DESC);
CREATE INDEX audit_logs_action_occurred_at_idx
    ON timeseries.audit_logs (action, occurred_at DESC);
CREATE INDEX audit_logs_actor_idx
    ON timeseries.audit_logs (actor_type, actor_id, occurred_at DESC);

COMMENT ON TABLE timeseries.audit_logs IS
    'Append-only audit trail. Monthly RANGE partitions maintained by pg_partman.';

-- +goose StatementEnd

-- +goose StatementBegin
-- Hand the partition set to pg_partman. p_premake keeps future months ready so
-- an insert never lands in the default partition during normal operation.
SELECT partman.create_parent(
    p_parent_table          => 'timeseries.audit_logs',
    p_control               => 'occurred_at',
    p_interval              => '1 month',
    p_type                  => 'range',
    p_premake               => 4,
    p_default_table         => true,
    p_automatic_maintenance => 'on'
);
-- +goose StatementEnd

-- +goose StatementBegin
-- Retention: drop audit children older than a year, and actually DROP them
-- rather than leaving detached tables behind.
UPDATE partman.part_config
   SET retention                = '12 months',
       retention_keep_table     = false,
       retention_keep_index     = false,
       infinite_time_partitions = true
 WHERE parent_table = 'timeseries.audit_logs';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM partman.part_config WHERE parent_table = 'timeseries.audit_logs';
DROP TABLE IF EXISTS timeseries.audit_logs CASCADE;
-- +goose StatementEnd
