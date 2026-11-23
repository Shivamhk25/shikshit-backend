-- 000027_notifications: in-app notifications (FND-16). One row per recipient. The institute boundary is enforced by
-- row-level security; "only your own rows" is enforced by every query in internal/platform/notify (the session does
-- not carry the user id) and by a composite foreign key that ties the recipient to the same institute.

CREATE TABLE notifications (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL,
    kind       text NOT NULL CHECK (char_length(kind) BETWEEN 1 AND 60),
    title      text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
    body       text NOT NULL DEFAULT '' CHECK (char_length(body) <= 500),
    link       text CHECK (link IS NULL OR (link LIKE '/%' AND link NOT LIKE '//%' AND char_length(link) <= 300)),
    created_at timestamptz NOT NULL DEFAULT now(),
    read_at    timestamptz,
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);
COMMENT ON TABLE notifications IS 'In-app notifications (FND-16), one row per recipient. link is a relative path inside the app.';
CREATE INDEX notifications_user_idx ON notifications (tenant_id, user_id, created_at DESC, id DESC);
CREATE INDEX notifications_unread_idx ON notifications (tenant_id, user_id) WHERE read_at IS NULL;
CREATE INDEX notifications_read_idx ON notifications (read_at) WHERE read_at IS NOT NULL;
ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;
CREATE POLICY notifications_tenant ON notifications USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
-- The hourly clean-up runs as the platform. It may only delete (a DELETE with a WHERE also needs to see the rows).
CREATE POLICY notifications_platform_see ON notifications FOR SELECT USING (app_is_platform());
CREATE POLICY notifications_platform_purge ON notifications FOR DELETE USING (app_is_platform());
