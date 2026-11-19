DELETE FROM role_template_permissions;
DELETE FROM role_templates;
DELETE FROM permissions;
DELETE FROM plans WHERE code IN ('standard', 'premium', 'enterprise');
