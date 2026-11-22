DROP TABLE IF EXISTS platform_user_invites;
UPDATE platform_users SET status = 'disabled' WHERE status = 'invited';
ALTER TABLE platform_users DROP CONSTRAINT platform_users_status_check;
ALTER TABLE platform_users ADD CONSTRAINT platform_users_status_check CHECK (status IN ('active', 'disabled'));
