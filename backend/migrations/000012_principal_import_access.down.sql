-- Removes the three grants from the template and from roles that still carry it. A grant an institute added by hand to
-- its Principal role is indistinguishable from this one and is removed too.
DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id AND r.template_code = 'principal' AND NOT r.is_system
  AND rp.permission_code IN ('onb.workspace.view', 'onb.imports.*', 'onb.academic.*');

DELETE FROM role_template_permissions
WHERE template_code = 'principal' AND permission_code IN ('onb.workspace.view', 'onb.imports.*', 'onb.academic.*');
