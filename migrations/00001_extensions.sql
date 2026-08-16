-- +goose Up
-- +goose StatementBegin

-- pgcrypto supplies gen_random_uuid() for primary keys.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- pg_partman manages child-table creation and retention for the high-volume
-- time-series tables. It lives in its own schema so its config tables never
-- collide with application tables.
CREATE SCHEMA IF NOT EXISTS partman;
CREATE EXTENSION IF NOT EXISTS pg_partman WITH SCHEMA partman;

-- Application schema for time-series children. Keeping partitions out of
-- public makes \dt readable once hundreds of child tables exist.
CREATE SCHEMA IF NOT EXISTS timeseries;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP SCHEMA IF EXISTS timeseries CASCADE;
DROP EXTENSION IF EXISTS pg_partman;
DROP SCHEMA IF EXISTS partman CASCADE;
-- pgcrypto is intentionally left installed: other databases in the cluster or
-- future migrations may depend on it, and dropping it is not reversible-safe.
-- +goose StatementEnd
