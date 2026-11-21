-- The Principal owns the academic setup (ONB-10): let it open the onboarding workspace and import and commit academic data.
-- Applies to the template (new institutes) and to existing institutes whose Principal role is still the template role.
-- Idempotent. Adding grants bumps the access version of every holder, so signed-in principals pick it up on their next call.
INSERT INTO role_template_permissions (template_code, permission_code, scope) VALUES
    ('principal', 'onb.workspace.view', 'tenant'),
    ('principal', 'onb.imports.*',      'tenant'),
    ('principal', 'onb.academic.*',     'tenant')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
SELECT r.tenant_id, r.id, tp.permission_code, tp.scope
FROM roles r
JOIN role_template_permissions tp ON tp.template_code = 'principal'
                                 AND tp.permission_code IN ('onb.workspace.view', 'onb.imports.*', 'onb.academic.*')
WHERE r.template_code = 'principal' AND r.deleted_at IS NULL AND NOT r.is_system
ON CONFLICT DO NOTHING;
