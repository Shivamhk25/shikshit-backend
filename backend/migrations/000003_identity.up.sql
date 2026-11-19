-- 000003_identity: platform users (Super Admin tier), tenant users, linked Google identities, sessions.
-- Roles are DATA (000004), never a column or enum here.

CREATE TABLE platform_users (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email              citext NOT NULL UNIQUE,
    display_name       text NOT NULL,
    password_hash      text,
    status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    mfa_enabled        boolean NOT NULL DEFAULT false,
    mfa_secret_enc     bytea,
    failed_login_count integer NOT NULL DEFAULT 0,
    locked_until       timestamptz,
    last_login_at      timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE platform_users IS 'Super Admin tier. Lives outside any institute. MFA is mandatory (enforced in the auth service).';
CREATE TRIGGER platform_users_updated_at BEFORE UPDATE ON platform_users FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE platform_users ENABLE ROW LEVEL SECURITY;
CREATE POLICY platform_users_platform ON platform_users FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

-- Pre-login lookup for the platform realm (the caller is anonymous, so RLS would hide the row).
CREATE OR REPLACE FUNCTION auth_lookup_platform_user(p_email text)
RETURNS TABLE (id uuid, display_name text, password_hash text, status text, mfa_enabled boolean,
               failed_login_count integer, locked_until timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS
$$
    SELECT u.id, u.display_name, u.password_hash, u.status, u.mfa_enabled, u.failed_login_count, u.locked_until
    FROM platform_users u WHERE u.email = p_email::citext
$$;
REVOKE ALL ON FUNCTION auth_lookup_platform_user(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_lookup_platform_user(text) TO shikshit_app;

CREATE TABLE users (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    username            citext NOT NULL CHECK (length(username) BETWEEN 3 AND 64),
    email               citext,
    phone               text,
    display_name        text NOT NULL,
    password_hash       text,
    status              text NOT NULL DEFAULT 'invited' CHECK (status IN ('invited', 'active', 'locked', 'disabled')),
    person_type         text CHECK (person_type IN ('student', 'guardian', 'staff')),
    person_id           uuid,
    must_change_password boolean NOT NULL DEFAULT false,
    mfa_enabled         boolean NOT NULL DEFAULT false,
    mfa_secret_enc      bytea,
    failed_login_count  integer NOT NULL DEFAULT 0,
    locked_until        timestamptz,
    last_login_at       timestamptz,
    access_version      bigint NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE users IS 'Institute users. Username/email are unique per institute. password_hash is NULL for Google-only users.';
COMMENT ON COLUMN users.access_version IS 'Incremented whenever this user''s roles, grants or scopes (or the institute''s modules) change; surfaced as X-Access-Version.';
CREATE UNIQUE INDEX users_tenant_username_uq ON users (tenant_id, username) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX users_tenant_email_uq ON users (tenant_id, email) WHERE email IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX users_tenant_status_idx ON users (tenant_id, status);
CREATE INDEX users_tenant_person_idx ON users (tenant_id, person_type, person_id) WHERE person_id IS NOT NULL;
CREATE TRIGGER users_updated_at BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
CREATE POLICY users_tenant ON users USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE user_identities (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL,
    user_id        uuid NOT NULL,
    provider       text NOT NULL CHECK (provider IN ('google')),
    subject        text NOT NULL,
    email          citext,
    email_verified boolean NOT NULL DEFAULT false,
    linked_at      timestamptz NOT NULL DEFAULT now(),
    last_used_at   timestamptz,
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, provider, subject)
);
COMMENT ON TABLE user_identities IS 'External sign-in identities (Google). Linking requires a verified email of an existing or invited user; never auto-creates users.';
CREATE INDEX user_identities_user_idx ON user_identities (tenant_id, user_id);
ALTER TABLE user_identities ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_identities_tenant ON user_identities USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid REFERENCES tenants(id) ON DELETE CASCADE,
    user_id          uuid,
    platform_user_id uuid REFERENCES platform_users(id) ON DELETE CASCADE,
    family_id        uuid NOT NULL,
    token_hash       text NOT NULL UNIQUE,
    client           text NOT NULL CHECK (client IN ('web', 'ios', 'android')),
    client_version   text,
    ip               inet,
    user_agent       text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_used_at     timestamptz,
    expires_at       timestamptz NOT NULL,
    revoked_at       timestamptz,
    replaced_by      uuid,
    CHECK (
        (user_id IS NOT NULL AND tenant_id IS NOT NULL AND platform_user_id IS NULL)
        OR (platform_user_id IS NOT NULL AND user_id IS NULL AND tenant_id IS NULL)
    ),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);
COMMENT ON TABLE sessions IS 'Refresh-token sessions with rotation families; token_hash is a hash, never the token. Platform sessions have tenant_id NULL.';
CREATE INDEX sessions_user_idx ON sessions (tenant_id, user_id) WHERE revoked_at IS NULL;
CREATE INDEX sessions_family_idx ON sessions (family_id);
CREATE INDEX sessions_expiry_idx ON sessions (expires_at);
ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY sessions_tenant ON sessions USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
CREATE POLICY sessions_platform ON sessions USING (tenant_id IS NULL AND app_is_platform()) WITH CHECK (tenant_id IS NULL AND app_is_platform());
