-- 000038_student_pauses: a student is away for a while and is expected back (STU-09). While a pause is active the
-- student's status is "paused"; ending or cancelling the pause sets it back to "active" in the same transaction (the
-- service does that; the trigger below only covers a status changed by other means). The Alumni list (STU-10) is read
-- from existing data and adds no table.
-- Reasons for a pause include health and money, so the records are personal data of a child:
--   * only tenant-wide holders of stu.student_pauses.view may read them (the API refuses class-section grants);
--   * the built-in roles that read every student record for another reason (accountant, librarian, ...) no longer hold
--     this view permission;
--   * every list and read is audited.

CREATE TABLE student_pauses (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    student_id    uuid NOT NULL,
    -- the enrolment the student had when paused; shows the class in lists and lets the list be filtered by class
    enrolment_id  uuid,
    paused_from   date NOT NULL,
    expected_back date,
    reason        text NOT NULL CHECK (reason IN ('illness', 'family_relocation', 'financial', 'family_emergency', 'other')),
    reason_note   text CHECK (reason_note IS NULL OR length(reason_note) <= 1000),
    status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'ended', 'cancelled')),
    -- the day the student came back; only an ended pause has one
    ended_on      date,
    -- when and by whom the pause was ended or cancelled (null when closed by the trigger below)
    closed_at     timestamptz,
    closed_by     uuid,
    close_note    text CHECK (close_note IS NULL OR length(close_note) <= 500),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    created_by    uuid,
    updated_by    uuid,
    deleted_at    timestamptz,
    deleted_by    uuid,
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, student_id)   REFERENCES students (tenant_id, id),
    FOREIGN KEY (tenant_id, enrolment_id) REFERENCES student_enrollments (tenant_id, id),
    CONSTRAINT student_pauses_expected_ck CHECK (expected_back IS NULL OR expected_back >= paused_from),
    CONSTRAINT student_pauses_ended_ck    CHECK ((status = 'ended') = (ended_on IS NOT NULL) AND (ended_on IS NULL OR ended_on >= paused_from)),
    CONSTRAINT student_pauses_closed_ck   CHECK ((status = 'active') = (closed_at IS NULL)),
    -- a reason of "other" must be explained
    CONSTRAINT student_pauses_other_note_ck CHECK (reason <> 'other' OR length(btrim(COALESCE(reason_note, ''))) > 0)
);
COMMENT ON TABLE student_pauses IS 'A student''s temporary absence from school (STU-09). While active, students.status is paused. Personal data of a child: reads are audited. Rows are never deleted: a mistaken pause is cancelled.';

-- one active pause per student
CREATE UNIQUE INDEX student_pauses_active_uq ON student_pauses (tenant_id, student_id) WHERE status = 'active' AND deleted_at IS NULL;
CREATE INDEX student_pauses_list_idx ON student_pauses (tenant_id, status, paused_from DESC) WHERE deleted_at IS NULL;
CREATE INDEX student_pauses_student_idx ON student_pauses (tenant_id, student_id) WHERE deleted_at IS NULL;

CREATE TRIGGER student_pauses_updated_at BEFORE UPDATE ON student_pauses FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE student_pauses ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_pauses_tenant ON student_pauses USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

-- A student who leaves "paused" by any other route (a transfer completed while paused, a status edited on the student
-- form) must not keep an active pause behind: the list would then show someone as away who is not. The pause is ended
-- on the day of the change (never before it began). The service closes the pause before it changes the student, so
-- this finds nothing to do for its own changes.
CREATE FUNCTION student_pauses_close_on_status() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE student_pauses
    SET status = 'ended', ended_on = GREATEST(paused_from, (now() AT TIME ZONE 'Asia/Kolkata')::date), closed_at = now(),
        close_note = 'Ended automatically: the student became ' || NEW.status
    WHERE tenant_id = NEW.tenant_id AND student_id = NEW.id AND status = 'active' AND deleted_at IS NULL;
    RETURN NULL;
END $$;
CREATE TRIGGER students_close_pauses AFTER UPDATE OF status ON students FOR EACH ROW
    WHEN (OLD.status = 'paused' AND NEW.status <> 'paused') EXECUTE FUNCTION student_pauses_close_on_status();

-- The permissions are already in the catalog (000007). The roles that read every student record for another reason
-- (accountant, librarian, transport manager, security guard, nurse, counselor) hold stu.student_pauses.view since
-- 000035 turned their wildcard into per-resource grants. Reasons for a pause include health and money, so they lose
-- this one, like the transfers. Nobody could have used it before this migration. The principal and the registrar keep
-- theirs; a teacher's or warden's narrower grant cannot reach pauses (the API needs the whole school). The Alumni
-- permission is left as it is: the list shows names, last class and exit date, never why a child left.
DELETE FROM role_template_permissions
WHERE permission_code = 'stu.student_pauses.view'
  AND template_code IN ('accountant', 'librarian', 'transport_manager', 'security_guard', 'nurse', 'counselor');

DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id AND rp.tenant_id = r.tenant_id AND rp.permission_code = 'stu.student_pauses.view'
  AND r.template_code IN ('accountant', 'librarian', 'transport_manager', 'security_guard', 'nurse', 'counselor')
  AND NOT r.is_system AND r.deleted_at IS NULL;
