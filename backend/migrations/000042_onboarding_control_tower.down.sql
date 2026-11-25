DROP TABLE IF EXISTS onboarding_domains;
DROP TABLE IF EXISTS onboarding_plans;

DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id AND r.template_code = 'principal' AND NOT r.is_system AND rp.permission_code = 'onb.control_tower.*';
DELETE FROM role_template_permissions WHERE template_code = 'principal' AND permission_code = 'onb.control_tower.*';
DELETE FROM role_permissions WHERE permission_code = 'onb.control_tower.edit';
DELETE FROM role_template_permissions WHERE permission_code = 'onb.control_tower.edit';
DELETE FROM permissions WHERE code = 'onb.control_tower.edit';
