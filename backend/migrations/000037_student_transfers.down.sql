-- Give the six roles their transfers view back, with the scope of their student view, then drop everything.
INSERT INTO role_template_permissions (template_code, permission_code, scope)
SELECT t.template_code, 'stu.student_transfers.view', t.scope
FROM role_template_permissions t
WHERE t.permission_code = 'stu.students.view'
  AND t.template_code IN ('accountant', 'librarian', 'transport_manager', 'security_guard', 'nurse', 'counselor')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
SELECT rp.tenant_id, rp.role_id, 'stu.student_transfers.view', rp.scope
FROM role_permissions rp
JOIN roles r ON r.id = rp.role_id AND r.tenant_id = rp.tenant_id
WHERE rp.permission_code = 'stu.students.view'
  AND r.template_code IN ('accountant', 'librarian', 'transport_manager', 'security_guard', 'nurse', 'counselor')
  AND NOT r.is_system AND r.deleted_at IS NULL
ON CONFLICT DO NOTHING;

DROP TABLE student_transfers;
DROP FUNCTION student_transfers_before_write();
DROP FUNCTION next_tc_number(uuid);
DROP TABLE transfer_certificate_counters;
