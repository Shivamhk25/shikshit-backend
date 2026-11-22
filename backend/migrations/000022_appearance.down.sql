DROP FUNCTION IF EXISTS set_tenant_appearance(text, text);
ALTER TABLE users DROP COLUMN IF EXISTS preferences;
DELETE FROM role_permissions WHERE permission_code IN ('iam.appearance.view', 'iam.appearance.manage');
DELETE FROM user_permission_overrides WHERE permission_code IN ('iam.appearance.view', 'iam.appearance.manage');
DELETE FROM permissions WHERE code IN ('iam.appearance.view', 'iam.appearance.manage');
