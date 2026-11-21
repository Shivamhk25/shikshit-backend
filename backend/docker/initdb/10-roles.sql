-- DEVELOPMENT ONLY (docker-compose). Runs once when the Postgres volume is first created.
--
-- The compose superuser (shikshit) bypasses row-level security, so the API must NOT connect as that user if you
-- want to exercise tenant isolation locally. Connect as shikshit_api instead: it is a member of shikshit_app,
-- which is not a superuser and has no BYPASSRLS. Migrations still run as the owner (shikshit).
-- Production uses a secrets-managed password for the same role layout.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'shikshit_app') THEN
        CREATE ROLE shikshit_app NOLOGIN NOSUPERUSER NOBYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'shikshit_api') THEN
        CREATE ROLE shikshit_api LOGIN PASSWORD 'shikshit_api_dev_password' IN ROLE shikshit_app;
    END IF;
END
$$;
