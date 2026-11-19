-- 000001_extensions: extensions available in stock Postgres 16, the runtime DB role, and RLS helper functions.
-- Optional extensions (vector, timescaledb, pgaudit) are NOT required; add them in the migration of the module that needs them.

CREATE EXTENSION IF NOT EXISTS citext;   -- case-insensitive usernames, emails, codes
CREATE EXTENSION IF NOT EXISTS pg_trgm;  -- fuzzy search (palette, student search)

-- The API connects with a login role that is a member of shikshit_app. That role is NOT a superuser
-- and has no BYPASSRLS, so row-level security applies to every request.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'shikshit_app') THEN
        CREATE ROLE shikshit_app NOLOGIN NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO shikshit_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO shikshit_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO shikshit_app;

-- Per-request context. The API sets these with set_config(..., true) inside a transaction (pgbouncer-safe):
--   app.tenant_id   the institute the request is bound to
--   app.actor_tier  'tenant' | 'platform'   (anything else is treated as anonymous)
CREATE OR REPLACE FUNCTION app_current_tenant() RETURNS uuid
    LANGUAGE sql STABLE AS
$$ SELECT nullif(current_setting('app.tenant_id', true), '')::uuid $$;

CREATE OR REPLACE FUNCTION app_actor_tier() RETURNS text
    LANGUAGE sql STABLE AS
$$ SELECT coalesce(nullif(current_setting('app.actor_tier', true), ''), 'anonymous') $$;

CREATE OR REPLACE FUNCTION app_is_platform() RETURNS boolean
    LANGUAGE sql STABLE AS
$$ SELECT app_actor_tier() = 'platform' $$;

CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END
$$;
