DROP TABLE IF EXISTS mfa_recovery_codes;
DROP TABLE IF EXISTS mfa_challenges;
ALTER TABLE users DROP COLUMN IF EXISTS mfa_last_step;
