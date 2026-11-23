-- 000037_student_transfers: a student leaves the school (STU-08). A transfer is requested, approved through the
-- approval engine (FND-20), and completed: completing gives the transfer certificate (TC) number, marks the student
-- "transferred" and closes the student's enrolment. Reasons include health and money, so the records are personal data:
--   * only tenant-wide holders of stu.student_transfers.view may read them (the API refuses class-section grants);
--   * the built-in roles that read every student record for another reason (accountant, librarian, ...) no longer hold
--     this view permission;
--   * every list, read and certificate is audited.

-- One counter per institute, like student_code_counters: the row lock makes concurrent completions take turns, and the
-- increment commits or rolls back with the completion that took it, so a TC number is never lost or given twice.
CREATE TABLE transfer_certificate_counters (
    tenant_id  uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    last_value integer NOT NULL DEFAULT 0 CHECK (last_value >= 0)
);
COMMENT ON TABLE transfer_certificate_counters IS 'Last transfer certificate number given, per institute (STU-08). The TC number format is TC-000012.';
ALTER TABLE transfer_certificate_counters ENABLE ROW LEVEL SECURITY;
CREATE POLICY transfer_certificate_counters_tenant ON transfer_certificate_counters USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE student_transfers (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    student_id          uuid NOT NULL,
    -- the enrolment the student had when the transfer was requested; shows the class in lists and on the certificate
    enrolment_id        uuid,
    requested_on        date NOT NULL,
    reason              text NOT NULL CHECK (reason IN ('relocation', 'another_school', 'financial', 'health', 'other')),
    reason_note         text CHECK (reason_note IS NULL OR length(reason_note) <= 1000),
    destination_school  text CHECK (destination_school IS NULL OR length(destination_school) <= 200),
    last_attendance_date date,
    status              text NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'approved', 'rejected', 'cancelled', 'completed')),
    approval_request_id uuid,
    approved_at         timestamptz,
    approved_by         uuid,
    rejected_at         timestamptz,
    cancelled_at        timestamptz,
    cancelled_by        uuid,
    completed_at        timestamptz,
    completed_by        uuid,
    tc_number           text,
    tc_issued_on        date,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    created_by          uuid,
    updated_by          uuid,
    deleted_at          timestamptz,
    deleted_by          uuid,
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, student_id)          REFERENCES students (tenant_id, id),
    FOREIGN KEY (tenant_id, enrolment_id)        REFERENCES student_enrollments (tenant_id, id),
    FOREIGN KEY (tenant_id, approval_request_id) REFERENCES approval_requests (tenant_id, id),
    -- a reason of "other" must be explained
    CONSTRAINT student_transfers_other_note_ck CHECK (reason <> 'other' OR length(btrim(COALESCE(reason_note, ''))) > 0),
    -- the certificate number, its date and the completion time exist together, and only on a completed transfer
    CONSTRAINT student_transfers_completed_ck CHECK ((status = 'completed') = (tc_number IS NOT NULL AND tc_issued_on IS NOT NULL AND completed_at IS NOT NULL))
);
COMMENT ON TABLE student_transfers IS 'A request for a student to leave the school (STU-08). Personal data of a child: reads are audited. The TC number is given by a trigger when the transfer becomes completed and never changes; numbers are never reused.';

-- at most one open transfer per student
CREATE UNIQUE INDEX student_transfers_open_uq ON student_transfers (tenant_id, student_id) WHERE status IN ('requested', 'approved') AND deleted_at IS NULL;
-- a TC number is never reused, so this index covers deleted rows too
CREATE UNIQUE INDEX student_transfers_tc_uq ON student_transfers (tenant_id, lower(tc_number)) WHERE tc_number IS NOT NULL;
CREATE INDEX student_transfers_list_idx ON student_transfers (tenant_id, status, requested_on DESC) WHERE deleted_at IS NULL;
CREATE INDEX student_transfers_student_idx ON student_transfers (tenant_id, student_id) WHERE deleted_at IS NULL;

-- Next TC number (TC-000012), the same shape as next_student_code. A number already used (even by a deleted row) is skipped.
CREATE FUNCTION next_tc_number(p_tenant uuid) RETURNS text LANGUAGE plpgsql AS $$
DECLARE
    n    integer;
    code text;
BEGIN
    LOOP
        INSERT INTO transfer_certificate_counters (tenant_id, last_value) VALUES (p_tenant, 1)
        ON CONFLICT (tenant_id) DO UPDATE SET last_value = transfer_certificate_counters.last_value + 1
        RETURNING last_value INTO n;
        code := 'TC-' || lpad(n::text, 6, '0');
        EXIT WHEN NOT EXISTS (SELECT 1 FROM student_transfers WHERE tenant_id = p_tenant AND lower(tc_number) = lower(code));
    END LOOP;
    RETURN code;
END $$;

-- The API can never type a TC number: the trigger gives it when a transfer becomes completed, keeps it unchanged
-- afterwards, and clears any value sent on insert. A completed transfer cannot be reopened, moved to another student or
-- deleted.
CREATE FUNCTION student_transfers_before_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.tc_number := NULL;
        NEW.tc_issued_on := NULL;
        NEW.completed_at := NULL;
        NEW.completed_by := NULL;
        IF NEW.status = 'completed' THEN
            RAISE EXCEPTION 'a transfer starts as requested, not completed' USING ERRCODE = '23514';
        END IF;
    ELSIF OLD.status = 'completed' THEN
        IF NEW.status <> 'completed' OR NEW.student_id <> OLD.student_id OR NEW.deleted_at IS NOT NULL THEN
            RAISE EXCEPTION 'a completed transfer cannot be changed or deleted' USING ERRCODE = '23514';
        END IF;
        NEW.tc_number := OLD.tc_number;
        NEW.tc_issued_on := OLD.tc_issued_on;
        NEW.completed_at := OLD.completed_at;
        NEW.completed_by := OLD.completed_by;
    ELSIF NEW.status = 'completed' THEN
        NEW.tc_number := next_tc_number(NEW.tenant_id);
    ELSE
        NEW.tc_number := NULL;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER student_transfers_tc BEFORE INSERT OR UPDATE ON student_transfers FOR EACH ROW EXECUTE FUNCTION student_transfers_before_write();
CREATE TRIGGER student_transfers_updated_at BEFORE UPDATE ON student_transfers FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE student_transfers ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_transfers_tenant ON student_transfers USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

-- The permissions are already in the catalog (000007). The roles that read every student record for another reason
-- (accountant, librarian, transport manager, security guard, nurse, counselor) hold stu.student_transfers.view since
-- 000035 turned their wildcard into per-resource grants. Reasons for leaving include health and money, so they lose
-- this one. Nobody could have used it before this migration, so no school's own choice is undone. The principal and the
-- registrar keep theirs; a teacher's or warden's narrower grant cannot reach transfers (the API needs the whole school).
DELETE FROM role_template_permissions
WHERE permission_code = 'stu.student_transfers.view'
  AND template_code IN ('accountant', 'librarian', 'transport_manager', 'security_guard', 'nurse', 'counselor');

DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id AND rp.tenant_id = r.tenant_id AND rp.permission_code = 'stu.student_transfers.view'
  AND r.template_code IN ('accountant', 'librarian', 'transport_manager', 'security_guard', 'nurse', 'counselor')
  AND NOT r.is_system AND r.deleted_at IS NULL;
