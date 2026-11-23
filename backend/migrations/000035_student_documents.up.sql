-- 000035_student_documents: documents kept on a student's record (STU-03): birth certificate, transfer certificate,
-- previous report card, parent ID proof and the like. The files themselves live in object storage (decision 0020,
-- purpose student_document); this table says what each file is, whose it is, and whether the school has checked it.
-- These are the most sensitive records a school holds about a child and family, so:
--   * only tenant-wide holders of stu.student_documents.view may read them (the API refuses class-section grants);
--   * the built-in roles that held every stu.*.view (accountant, librarian, ...) no longer hold this one;
--   * every list and download is audited; a deleted document is removed from storage, its row is kept as history.
-- Left out on purpose: caste/category/religion certificates (decision 0009) and disability certificates (waiting for the
-- founder's answer, APPROVALS).

ALTER TABLE files DROP CONSTRAINT files_purpose_check;
ALTER TABLE files ADD CONSTRAINT files_purpose_check CHECK (purpose IN ('student_photo', 'student_document'));

CREATE TABLE student_documents (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    student_id    uuid NOT NULL,
    document_type text NOT NULL CHECK (document_type IN ('birth_certificate', 'transfer_certificate', 'previous_marksheet',
                      'migration_certificate', 'medical_certificate', 'parent_id_proof', 'address_proof', 'other')),
    title         text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 120),
    file_id       uuid NOT NULL,
    valid_until   date,
    uploaded_by   uuid,
    uploaded_at   timestamptz NOT NULL DEFAULT now(),
    verified_by   uuid,
    verified_at   timestamptz,
    deleted_at    timestamptz,
    deleted_by    uuid,
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, file_id),
    FOREIGN KEY (tenant_id, student_id) REFERENCES students (tenant_id, id),
    FOREIGN KEY (tenant_id, file_id) REFERENCES files (tenant_id, id),
    CHECK (verified_at IS NOT NULL OR verified_by IS NULL)
);
COMMENT ON TABLE student_documents IS 'Documents on a student record (STU-03). The bytes are in files (purpose student_document). Soft-deleted rows keep who and when; the object is removed from storage.';
CREATE INDEX student_documents_student_idx ON student_documents (tenant_id, student_id, uploaded_at DESC) WHERE deleted_at IS NULL;
ALTER TABLE student_documents ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_documents_tenant ON student_documents USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

-- The permissions are already in the catalog (000007). Roles that hold stu.*.view for another reason (accountant,
-- librarian, transport manager, security guard, nurse, counselor) must not read a child's certificates and parents' ID
-- proofs by accident, so their wildcard becomes the same grants without student_documents. The principal and the
-- registrar keep theirs; the teacher's class-section grant cannot reach documents (the API needs the whole school).
-- Applies to the templates (new institutes) and to existing institutes' unedited copies of those roles.
WITH removed AS (
    DELETE FROM role_template_permissions
    WHERE permission_code = 'stu.*.view'
      AND template_code IN ('accountant', 'librarian', 'transport_manager', 'security_guard', 'nurse', 'counselor')
    RETURNING template_code, scope
)
INSERT INTO role_template_permissions (template_code, permission_code, scope)
SELECT r.template_code, p.code, r.scope
FROM removed r
JOIN permissions p ON p.module = 'stu' AND p.action = 'view' AND p.resource <> 'student_documents'
ON CONFLICT DO NOTHING;

WITH removed AS (
    DELETE FROM role_permissions rp
    USING roles r
    WHERE rp.role_id = r.id AND rp.tenant_id = r.tenant_id AND rp.permission_code = 'stu.*.view'
      AND r.template_code IN ('accountant', 'librarian', 'transport_manager', 'security_guard', 'nurse', 'counselor')
      AND NOT r.is_system AND r.deleted_at IS NULL
    RETURNING rp.tenant_id, rp.role_id, rp.scope
)
INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
SELECT r.tenant_id, r.role_id, p.code, r.scope
FROM removed r
JOIN permissions p ON p.module = 'stu' AND p.action = 'view' AND p.resource <> 'student_documents'
ON CONFLICT DO NOTHING;
