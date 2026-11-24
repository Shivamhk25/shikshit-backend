DROP TABLE IF EXISTS year_rollover_runs;
DELETE FROM tenant_modules WHERE module_code = 'ten';
UPDATE plans SET modules = array_remove(modules, 'ten');
DELETE FROM role_permissions WHERE permission_code LIKE 'ten.%';
DELETE FROM user_permission_overrides WHERE permission_code LIKE 'ten.%';
DELETE FROM permissions WHERE module = 'ten';
