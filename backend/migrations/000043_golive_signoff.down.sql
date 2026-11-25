DROP FUNCTION IF EXISTS onb_set_tenant_status(text, text);
DROP TABLE IF EXISTS golive_signoffs;

DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id AND r.template_code = 'principal' AND NOT r.is_system AND rp.permission_code = 'onb.golive.*';
DELETE FROM role_template_permissions WHERE permission_code = 'onb.golive.*';
DELETE FROM role_permissions WHERE permission_code IN ('onb.golive.view', 'onb.golive.signoff');
DELETE FROM role_template_permissions WHERE permission_code IN ('onb.golive.view', 'onb.golive.signoff');
DELETE FROM permissions WHERE code IN ('onb.golive.view', 'onb.golive.signoff');
