-- 000019_user_invites: one-time invite and reset-password tokens for institute users.
-- Tokens are random 32 bytes, stored only as a hash; a token works once.

CREATE TABLE user_invites (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id         uuid NOT NULL,
    token_hash      text NOT NULL UNIQUE,
    purpose         text NOT NULL CHECK (purpose IN ('invite', 'reset')),
    expires_at      timestamptz NOT NULL,
    used_at         timestamptz,
    created_by      uuid NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);
COMMENT ON TABLE user_invites IS 'One-time tokens for user invite (7 days) and password reset (24 hours). Only the hash is stored.';
CREATE INDEX user_invites_tenant_user_idx ON user_invites (tenant_id, user_id);
CREATE INDEX user_invites_expires_idx ON user_invites (expires_at);
ALTER TABLE user_invites ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_invites_tenant ON user_invites USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());