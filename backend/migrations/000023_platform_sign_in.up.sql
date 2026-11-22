-- 000023_platform_sign_in: sign-in for the platform (Super Admin) tier, decision 0017. Platform users always use
-- two-step sign-in: after a correct password they get a one-time challenge, to verify a code or (first time) to set
-- up their authenticator app. Platform sessions reuse the sessions table (tenant_id NULL).

ALTER TABLE platform_users ADD COLUMN mfa_last_step bigint;
COMMENT ON COLUMN platform_users.mfa_last_step IS 'TOTP time step of the last accepted code; a code for the same or an earlier step is refused.';

CREATE TABLE platform_mfa_challenges (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    platform_user_id   uuid NOT NULL REFERENCES platform_users(id) ON DELETE CASCADE,
    token_hash         text NOT NULL UNIQUE,
    kind               text NOT NULL CHECK (kind IN ('verify', 'setup')),
    pending_secret_enc bytea,
    attempts           integer NOT NULL DEFAULT 0,
    client             text NOT NULL DEFAULT 'web' CHECK (client IN ('web', 'ios', 'android')),
    client_version     text,
    expires_at         timestamptz NOT NULL,
    used_at            timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE platform_mfa_challenges IS 'Between a platform user''s correct password and their code (or first-time authenticator setup). token_hash is SHA-256 of the token. Single use, 10 minutes, at most 5 wrong codes.';
CREATE INDEX platform_mfa_challenges_user_idx ON platform_mfa_challenges (platform_user_id);
ALTER TABLE platform_mfa_challenges ENABLE ROW LEVEL SECURITY;
CREATE POLICY platform_mfa_challenges_platform ON platform_mfa_challenges FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());
