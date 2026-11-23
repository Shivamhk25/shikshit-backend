-- Restores the wildcard for the six roles, then removes the documents (their files are removed first).
WITH removed AS (
    DELETE FROM role_template_permissions
    WHERE template_code IN ('accountant', 'librarian', 'transport_manager', 'security_guard', 'nurse', 'counselor')
      AND permission_code LIKE 'stu.%.view' AND permission_code <> 'stu.*.view'
    RETURNING template_code, scope
)
INSERT INTO role_template_permissions (template_code, permission_code, scope)
SELECT DISTINCT template_code, 'stu.*.view', scope FROM removed
ON CONFLICT DO NOTHING;

WITH removed AS (
    DELETE FROM role_permissions rp
    USING roles r
    WHERE rp.role_id = r.id AND rp.tenant_id = r.tenant_id
      AND r.template_code IN ('accountant', 'librarian', 'transport_manager', 'security_guard', 'nurse', 'counselor')
      AND NOT r.is_system AND r.deleted_at IS NULL
      AND rp.permission_code LIKE 'stu.%.view' AND rp.permission_code <> 'stu.*.view'
    RETURNING rp.tenant_id, rp.role_id, rp.scope
)
INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
SELECT DISTINCT tenant_id, role_id, 'stu.*.view', scope FROM removed
ON CONFLICT DO NOTHING;

DROP TABLE IF EXISTS student_documents;
DELETE FROM files WHERE purpose = 'student_document';
ALTER TABLE files DROP CONSTRAINT files_purpose_check;
ALTER TABLE files ADD CONSTRAINT files_purpose_check CHECK (purpose IN ('student_photo'));
