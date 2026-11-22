-- 000020_mfa: two-step sign-in (FND-05). TOTP secrets stay in users.mfa_secret_enc (AES-GCM, key outside the
-- database); this adds the one-time challenge between the password and the code, recovery codes, and the last code
-- step used so a code cannot be replayed.

ALTER TABLE users ADD COLUMN mfa_last_step bigint;
COMMENT ON COLUMN users.mfa_last_step IS 'TOTP time step of the last accepted code; a code for the same or an earlier step is refused.';

CREATE TABLE mfa_challenges (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id            uuid NOT NULL,
    token_hash         text UNIQUE,
    kind               text NOT NULL CHECK (kind IN ('verify', 'setup', 'enroll')),
    pending_secret_enc bytea,
    attempts           integer NOT NULL DEFAULT 0,
    client             text NOT NULL DEFAULT 'web' CHECK (client IN ('web', 'ios', 'android')),
    client_version     text,
    expires_at         timestamptz NOT NULL,
    used_at            timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);
COMMENT ON TABLE mfa_challenges IS 'Between a correct password and the second step (verify, or first-time setup), and a signed-in user setting up two-step sign-in (enroll). token_hash is SHA-256 of the token; enroll rows have none. Single use, 10 minutes, at most 5 wrong codes.';
CREATE INDEX mfa_challenges_user_idx ON mfa_challenges (tenant_id, user_id);
CREATE INDEX mfa_challenges_expires_idx ON mfa_challenges (expires_at);
ALTER TABLE mfa_challenges ENABLE ROW LEVEL SECURITY;
CREATE POLICY mfa_challenges_tenant ON mfa_challenges USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE mfa_recovery_codes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    user_id    uuid NOT NULL,
    code_hash  text NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (user_id, code_hash)
);
COMMENT ON TABLE mfa_recovery_codes IS 'One-time codes for when the authenticator phone is lost. Only SHA-256 of each code is stored.';
ALTER TABLE mfa_recovery_codes ENABLE ROW LEVEL SECURITY;
CREATE POLICY mfa_recovery_codes_tenant ON mfa_recovery_codes USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
