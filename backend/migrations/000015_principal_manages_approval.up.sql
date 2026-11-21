-- The Principal may change the institute's import-approval switch, as well as approve imports (decision, ONB-24).
-- Adds onb.maker_checker.* (view and manage) to the Principal template and to existing institutes' roles that still carry
-- it. Idempotent; adding grants bumps every holder's access version.
INSERT INTO role_template_permissions (template_code, permission_code, scope) VALUES
    ('principal', 'onb.maker_checker.*', 'tenant')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
SELECT r.tenant_id, r.id, tp.permission_code, tp.scope
FROM roles r
JOIN role_template_permissions tp ON tp.template_code = 'principal' AND tp.permission_code = 'onb.maker_checker.*'
WHERE r.template_code = 'principal' AND r.deleted_at IS NULL AND NOT r.is_system
ON CONFLICT DO NOTHING;
