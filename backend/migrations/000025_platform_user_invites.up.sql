-- 000025_platform_user_invites: SAD-04 platform users managed from the console (decision 0019). A new platform user is
-- "invited" until they set a password from a one-time link; they then set up two-step sign-in at first sign-in.

ALTER TABLE platform_users DROP CONSTRAINT platform_users_status_check;
ALTER TABLE platform_users ADD CONSTRAINT platform_users_status_check CHECK (status IN ('invited', 'active', 'disabled'));

CREATE TABLE platform_user_invites (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    platform_user_id uuid NOT NULL REFERENCES platform_users(id) ON DELETE CASCADE,
    token_hash       text NOT NULL UNIQUE,
    purpose          text NOT NULL CHECK (purpose IN ('invite', 'reset')),
    expires_at       timestamptz NOT NULL,
    used_at          timestamptz,
    created_by       uuid REFERENCES platform_users(id),
    created_at       timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE platform_user_invites IS 'One-time set-password links for platform users (invite 7 days, reset 24 hours). Only SHA-256 of the token is stored.';
CREATE INDEX platform_user_invites_user_idx ON platform_user_invites (platform_user_id);
ALTER TABLE platform_user_invites ENABLE ROW LEVEL SECURITY;
CREATE POLICY platform_user_invites_platform ON platform_user_invites FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());
