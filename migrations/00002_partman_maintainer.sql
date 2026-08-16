-- +goose Up
-- +goose NO TRANSACTION
-- +goose StatementBegin

-- pg_partman 5.5 changed the background-worker default role away from a
-- superuser (CVE-2026-61821 and related). We follow that: maintenance runs as
-- partman_maintainer, a LOGIN role with no superuser and no rights beyond what
-- partition maintenance needs.
--
-- The role is created here rather than in an init script so a fresh database is
-- reproducible from migrations alone. The password comes from the
-- app.partman_password setting when present; docker-compose supplies it.
DO $$
DECLARE
    v_password text := current_setting('app.partman_password', true);
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'partman_maintainer') THEN
        IF v_password IS NULL OR v_password = '' THEN
            -- No password configured: create the role without LOGIN. The BGW
            -- can still assume it via SET ROLE; nobody can connect as it.
            CREATE ROLE partman_maintainer;
        ELSE
            EXECUTE format('CREATE ROLE partman_maintainer WITH LOGIN PASSWORD %L', v_password);
        END IF;
    END IF;
END
$$;

-- +goose StatementEnd

-- +goose StatementBegin
GRANT USAGE, CREATE ON SCHEMA partman TO partman_maintainer;
GRANT ALL ON ALL TABLES IN SCHEMA partman TO partman_maintainer;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA partman TO partman_maintainer;
GRANT EXECUTE ON ALL PROCEDURES IN SCHEMA partman TO partman_maintainer;
GRANT USAGE, CREATE ON SCHEMA timeseries TO partman_maintainer;
-- +goose StatementEnd

-- +goose StatementBegin
-- Creating child tables needs temp tables when data must be moved out of the
-- default partition. GRANT ... ON DATABASE needs a literal name, so the
-- database name is interpolated rather than passed as CURRENT_CATALOG.
DO $$
BEGIN
    EXECUTE format('GRANT TEMPORARY ON DATABASE %I TO partman_maintainer', current_database());
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- The maintainer must be able to modify the partitioned tables it manages.
-- Granting the owning role to the maintainer is the mechanism pg_partman
-- documents for this; it is narrower than making the maintainer a superuser.
DO $$
BEGIN
    EXECUTE format('GRANT %I TO partman_maintainer', CURRENT_USER);
EXCEPTION WHEN OTHERS THEN
    -- Already a member, or CURRENT_USER cannot be granted (e.g. managed
    -- service). Maintenance still works when the owner runs it directly.
    RAISE NOTICE 'could not grant % to partman_maintainer: %', CURRENT_USER, SQLERRM;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose NO TRANSACTION
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'partman_maintainer') THEN
        -- Hand back anything the maintenance role came to own (child
        -- partitions created by the background worker) BEFORE dropping
        -- privileges. DROP OWNED alone would delete those tables and the audit
        -- history inside them.
        EXECUTE format('REASSIGN OWNED BY partman_maintainer TO %I', CURRENT_USER);
        -- With ownership reassigned, this only revokes grants. Postgres
        -- refuses DROP ROLE while any grant still references the role, and
        -- enumerating them by hand misses defaults and future objects.
        DROP OWNED BY partman_maintainer;
        EXECUTE format('REVOKE ALL ON DATABASE %I FROM partman_maintainer', current_database());
        DROP ROLE partman_maintainer;
    END IF;
END
$$;
-- +goose StatementEnd
