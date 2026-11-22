-- 000026_files: uploaded files (FND-17, decision 0020). The bytes live in object storage (S3-compatible); this table
-- records which institute owns each file, what it is for, who uploaded it and whether it passed the checks. Clients
-- upload straight to storage with a short-lived signed link; the API never carries the bytes.

CREATE TABLE files (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    purpose      text NOT NULL CHECK (purpose IN ('student_photo')),
    subject_id   uuid NOT NULL,
    object_key   text NOT NULL UNIQUE,
    content_type text NOT NULL,
    size_bytes   bigint NOT NULL CHECK (size_bytes > 0),
    status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'rejected', 'deleted')),
    created_by   uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    ready_at     timestamptz,
    deleted_at   timestamptz,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE files IS 'Uploaded files (FND-17). pending until the upload is checked (size and real content type); rejected objects are removed from storage. subject_id is the record the file belongs to (a student for student_photo).';
CREATE INDEX files_tenant_subject_idx ON files (tenant_id, purpose, subject_id);
CREATE INDEX files_pending_idx ON files (created_at) WHERE status = 'pending';
ALTER TABLE files ENABLE ROW LEVEL SECURITY;
CREATE POLICY files_tenant ON files USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

ALTER TABLE students ADD COLUMN photo_file_id uuid;
ALTER TABLE students ADD CONSTRAINT students_photo_file_fk FOREIGN KEY (tenant_id, photo_file_id) REFERENCES files (tenant_id, id);
COMMENT ON COLUMN students.photo_file_id IS 'The student''s photo (files, purpose student_photo, status ready).';
