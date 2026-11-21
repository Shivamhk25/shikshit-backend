DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id AND r.template_code = 'principal' AND NOT r.is_system AND rp.permission_code = 'onb.maker_checker.*';

DELETE FROM role_template_permissions WHERE template_code = 'principal' AND permission_code = 'onb.maker_checker.*';
