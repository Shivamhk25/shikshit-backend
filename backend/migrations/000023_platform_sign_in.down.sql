DROP TABLE IF EXISTS platform_mfa_challenges;
ALTER TABLE platform_users DROP COLUMN IF EXISTS mfa_last_step;
