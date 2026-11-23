-- Student promotions (STU-07): at the end of a year each class section is moved up, repeated or graduated. A batch is
-- the decision for ONE source class section from one academic year to the next: a row per student (promote, repeat,
-- graduate or leave out), the class and section to enrol in, and how roll numbers are given. A batch is a draft while
-- the registrar works on it, becomes committed when the next-year enrolments are made (all rows or none) and is undone
-- when the registrar takes the commit back inside the undo window.
--
-- Nothing here changes the enrolments of the year being closed. A promotion adds an enrolment in the new year; graduating
-- sets the student's status to alumni (the status the students table already allows). Undo removes the enrolments the
-- batch made and restores the student status it changed.
--
-- The permissions are already in the catalog (000007: stu.student_promotions.view/create/edit/delete) and the role
-- templates already reach them (registrar stu.*.*, principal and others stu.*.view). No grants change here.

CREATE TABLE promotion_batches (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    from_year_id    uuid NOT NULL,
    to_year_id      uuid NOT NULL,
    from_class_id   uuid NOT NULL,
    from_section_id uuid NOT NULL,
    roll_mode       text NOT NULL DEFAULT 'alphabetical' CHECK (roll_mode IN ('keep', 'alphabetical', 'blank')),
    note            text CHECK (note IS NULL OR length(note) <= 500),
    status          text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'committed', 'undone')),
    committed_at    timestamptz,
    committed_by    uuid,
    undo_until      timestamptz,
    -- set only when a dues gate blocked students and a person with the override right committed anyway
    dues_override_reason text CHECK (dues_override_reason IS NULL OR length(btrim(dues_override_reason)) BETWEEN 1 AND 500),
    undone_at       timestamptz,
    undone_by       uuid,
    undo_reason     text CHECK (undo_reason IS NULL OR length(undo_reason) <= 500),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    created_by      uuid,
    updated_by      uuid,
    deleted_at      timestamptz,
    deleted_by      uuid,
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, from_year_id)    REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, to_year_id)      REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, from_class_id)   REFERENCES classes (tenant_id, id),
    FOREIGN KEY (tenant_id, from_section_id) REFERENCES sections (tenant_id, id),
    CHECK (from_year_id <> to_year_id),
    CHECK ((status = 'draft') = (committed_at IS NULL AND undone_at IS NULL)),
    CHECK ((committed_at IS NULL) = (undo_until IS NULL)),
    CHECK ((status = 'undone') = (undone_at IS NOT NULL))
);
COMMENT ON TABLE promotion_batches IS 'One class section''s promotion from one academic year to the next (STU-07). Draft, then committed (next-year enrolments made, all or none), then possibly undone inside the undo window.';
-- one live batch per source class section and year pair; an undone batch frees the slot, a discarded draft too
CREATE UNIQUE INDEX promotion_batches_source_uq ON promotion_batches (tenant_id, from_year_id, to_year_id, from_class_id, from_section_id)
    WHERE status <> 'undone' AND deleted_at IS NULL;
CREATE INDEX promotion_batches_list_idx ON promotion_batches (tenant_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE TRIGGER promotion_batches_updated_at BEFORE UPDATE ON promotion_batches FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE promotion_batches ENABLE ROW LEVEL SECURITY;
CREATE POLICY promotion_batches_tenant ON promotion_batches USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE promotion_batch_students (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    batch_id              uuid NOT NULL,
    student_id            uuid NOT NULL,
    source_enrollment_id  uuid NOT NULL,
    decision              text NOT NULL CHECK (decision IN ('promote', 'repeat', 'graduate', 'leave_out')),
    to_class_id           uuid,
    to_section_id         uuid,
    -- a roll number typed for this student; it wins over the batch's roll mode
    roll_number           text CHECK (roll_number IS NULL OR length(btrim(roll_number)) BETWEEN 1 AND 20),
    -- filled by the commit, read by the undo
    new_enrollment_id     uuid,
    previous_student_status text CHECK (previous_student_status IS NULL OR previous_student_status IN ('active', 'paused', 'transferred', 'alumni')),
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, batch_id, student_id),
    FOREIGN KEY (tenant_id, batch_id)             REFERENCES promotion_batches (tenant_id, id),
    FOREIGN KEY (tenant_id, student_id)           REFERENCES students (tenant_id, id),
    FOREIGN KEY (tenant_id, source_enrollment_id) REFERENCES student_enrollments (tenant_id, id),
    FOREIGN KEY (tenant_id, to_class_id)          REFERENCES classes (tenant_id, id),
    FOREIGN KEY (tenant_id, to_section_id)        REFERENCES sections (tenant_id, id),
    FOREIGN KEY (tenant_id, new_enrollment_id)    REFERENCES student_enrollments (tenant_id, id),
    CHECK (decision IN ('promote', 'repeat') OR (to_class_id IS NULL AND to_section_id IS NULL AND roll_number IS NULL))
);
COMMENT ON TABLE promotion_batch_students IS 'The decision for one student in a promotion batch (STU-07), and after the commit the enrolment it made.';
CREATE INDEX promotion_batch_students_batch_idx ON promotion_batch_students (tenant_id, batch_id);
-- "was this enrolment promoted onward?" (undo) and "is this student in another live batch?"
CREATE INDEX promotion_batch_students_source_idx ON promotion_batch_students (tenant_id, source_enrollment_id);
CREATE INDEX promotion_batch_students_student_idx ON promotion_batch_students (tenant_id, student_id);
ALTER TABLE promotion_batch_students ENABLE ROW LEVEL SECURITY;
CREATE POLICY promotion_batch_students_tenant ON promotion_batch_students USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
