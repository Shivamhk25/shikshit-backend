-- 000039_student_electives: which elective subjects each student takes (STU-04) and requests to change one (STU-05).
--
-- An elective group (ACA-10) has only a minimum and a maximum; it never said which subjects belong to it ("subject
-- membership arrives with ACA-31"). The membership is now recorded where the subjects of a class already live: a
-- subject_class_mappings row may name the group it is an elective of. So the subjects a student may choose from a group
-- are the live mappings of that group for the student's class and year; a mapping with a section applies to that section
-- only and overrides the class-wide mapping of the same subject, exactly as it does for the timetable and marks.
--
-- A student has ONE choice per group and year: a row, plus one row per chosen subject. Every change to the set is kept in
-- the history table (who, when, before, after, and the change request that caused it). A change request swaps one chosen
-- subject for another once the approval engine (FND-20, type student_elective_change) has approved it.
--
-- The permissions are already in the catalog (000007). The teacher template holds stu.*.view, so teachers already read
-- electives and requests of their class sections; this migration lets a class teacher record choices and file change
-- requests for those sections (the API checks the student is in one of them) and lets the principal manage both and
-- decide requests. The registrar already holds stu.*.* and the admin everything.

ALTER TABLE subject_class_mappings ADD COLUMN elective_group_id uuid;
ALTER TABLE subject_class_mappings ADD CONSTRAINT subject_class_mappings_elective_group_fk FOREIGN KEY (tenant_id, elective_group_id) REFERENCES elective_groups (tenant_id, id);
ALTER TABLE subject_class_mappings ADD CONSTRAINT subject_class_mappings_group_needs_elective_ck CHECK (elective_group_id IS NULL OR is_elective);
COMMENT ON COLUMN subject_class_mappings.elective_group_id IS 'The elective group this subject is offered in for the class (and section) that year (STU-04). Only an elective mapping may name a group.';
CREATE INDEX subject_class_mappings_group_idx ON subject_class_mappings (tenant_id, elective_group_id, year_id, class_id) WHERE deleted_at IS NULL AND elective_group_id IS NOT NULL;

CREATE TABLE student_elective_choices (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    student_id uuid NOT NULL,
    year_id    uuid NOT NULL,
    group_id   uuid NOT NULL,
    status     text NOT NULL DEFAULT 'chosen' CHECK (status IN ('chosen', 'confirmed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    updated_by uuid,
    deleted_at timestamptz,
    deleted_by uuid,
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, student_id) REFERENCES students (tenant_id, id),
    FOREIGN KEY (tenant_id, year_id)    REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, group_id)   REFERENCES elective_groups (tenant_id, id)
);
COMMENT ON TABLE student_elective_choices IS 'The elective subjects one student takes from one group in one academic year (STU-04). The subjects are the rows of student_elective_choice_subjects; status says whether the school has confirmed the choice.';
-- one live choice per student, year and group
CREATE UNIQUE INDEX student_elective_choices_uq ON student_elective_choices (tenant_id, student_id, year_id, group_id) WHERE deleted_at IS NULL;
CREATE INDEX student_elective_choices_year_group_idx ON student_elective_choices (tenant_id, year_id, group_id) WHERE deleted_at IS NULL;
CREATE TRIGGER student_elective_choices_updated_at BEFORE UPDATE ON student_elective_choices FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE student_elective_choices ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_elective_choices_tenant ON student_elective_choices USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE student_elective_choice_subjects (
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    choice_id  uuid NOT NULL,
    subject_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    PRIMARY KEY (choice_id, subject_id),
    FOREIGN KEY (tenant_id, choice_id)  REFERENCES student_elective_choices (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, subject_id) REFERENCES subjects (tenant_id, id)
);
COMMENT ON TABLE student_elective_choice_subjects IS 'The subjects of one elective choice (STU-04): one row per subject. The set is replaced as a whole; the history table keeps what it was.';
CREATE INDEX student_elective_choice_subjects_subject_idx ON student_elective_choice_subjects (tenant_id, subject_id);
ALTER TABLE student_elective_choice_subjects ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_elective_choice_subjects_tenant ON student_elective_choice_subjects USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE student_elective_changes (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    student_id          uuid NOT NULL,
    choice_id           uuid NOT NULL,
    year_id             uuid NOT NULL,
    group_id            uuid NOT NULL,
    from_subject_id     uuid NOT NULL,
    to_subject_id       uuid NOT NULL,
    wanted_from         date NOT NULL,
    reason              text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 500),
    status              text NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'approved', 'rejected', 'cancelled')),
    approval_request_id uuid,
    decided_at          timestamptz,
    decided_by          uuid,
    cancelled_at        timestamptz,
    cancelled_by        uuid,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    created_by          uuid,
    updated_by          uuid,
    deleted_at          timestamptz,
    deleted_by          uuid,
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, student_id)          REFERENCES students (tenant_id, id),
    FOREIGN KEY (tenant_id, choice_id)           REFERENCES student_elective_choices (tenant_id, id),
    FOREIGN KEY (tenant_id, year_id)             REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, group_id)            REFERENCES elective_groups (tenant_id, id),
    FOREIGN KEY (tenant_id, from_subject_id)     REFERENCES subjects (tenant_id, id),
    FOREIGN KEY (tenant_id, to_subject_id)       REFERENCES subjects (tenant_id, id),
    FOREIGN KEY (tenant_id, approval_request_id) REFERENCES approval_requests (tenant_id, id),
    CONSTRAINT student_elective_changes_distinct_ck CHECK (from_subject_id <> to_subject_id),
    -- a decision exists exactly when the request was approved or rejected; a cancellation exactly when it was cancelled
    CONSTRAINT student_elective_changes_decided_ck CHECK ((status IN ('approved', 'rejected')) = (decided_at IS NOT NULL)),
    CONSTRAINT student_elective_changes_cancelled_ck CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL))
);
COMMENT ON TABLE student_elective_changes IS 'A request to swap one chosen elective subject for another (STU-05). Decided through the approval engine (type student_elective_change); approving swaps the subject in the choice and writes a history row in the same transaction.';
-- at most one open request per student, group, year and subject being replaced
CREATE UNIQUE INDEX student_elective_changes_open_uq ON student_elective_changes (tenant_id, student_id, year_id, group_id, from_subject_id) WHERE status = 'requested' AND deleted_at IS NULL;
CREATE INDEX student_elective_changes_list_idx ON student_elective_changes (tenant_id, status, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX student_elective_changes_student_idx ON student_elective_changes (tenant_id, student_id) WHERE deleted_at IS NULL;
CREATE INDEX student_elective_changes_choice_idx ON student_elective_changes (tenant_id, choice_id) WHERE deleted_at IS NULL;
CREATE TRIGGER student_elective_changes_updated_at BEFORE UPDATE ON student_elective_changes FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE student_elective_changes ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_elective_changes_tenant ON student_elective_changes USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE student_elective_choice_history (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    choice_id           uuid NOT NULL,
    student_id          uuid NOT NULL,
    year_id             uuid NOT NULL,
    group_id            uuid NOT NULL,
    action              text NOT NULL CHECK (action IN ('create', 'replace', 'swap', 'status', 'delete')),
    subjects_before     uuid[] NOT NULL DEFAULT '{}',
    subjects_after      uuid[] NOT NULL DEFAULT '{}',
    status_before       text,
    status_after        text,
    source              text NOT NULL CHECK (source IN ('staff', 'change_request')),
    change_id           uuid,
    approval_request_id uuid,
    changed_at          timestamptz NOT NULL DEFAULT now(),
    changed_by          uuid,
    FOREIGN KEY (tenant_id, choice_id)           REFERENCES student_elective_choices (tenant_id, id),
    FOREIGN KEY (tenant_id, student_id)          REFERENCES students (tenant_id, id),
    FOREIGN KEY (tenant_id, year_id)             REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, group_id)            REFERENCES elective_groups (tenant_id, id),
    FOREIGN KEY (tenant_id, change_id)           REFERENCES student_elective_changes (tenant_id, id),
    FOREIGN KEY (tenant_id, approval_request_id) REFERENCES approval_requests (tenant_id, id),
    -- the request that caused a change is named for a change made through a request, and only then
    CONSTRAINT student_elective_choice_history_source_ck CHECK ((source = 'change_request') = (change_id IS NOT NULL))
);
COMMENT ON TABLE student_elective_choice_history IS 'Every change to a student''s elective choice (STU-04/05): the subjects before and after, who changed them, and the change request behind it. Rows are only ever added.';
CREATE INDEX student_elective_choice_history_choice_idx ON student_elective_choice_history (tenant_id, choice_id, changed_at DESC);
CREATE INDEX student_elective_choice_history_student_idx ON student_elective_choice_history (tenant_id, student_id);
ALTER TABLE student_elective_choice_history ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_elective_choice_history_tenant ON student_elective_choice_history USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

-- Grants. Templates (new institutes) and the unedited copies of the roles in existing institutes.
--   * class teacher: records choices and files change requests, for the students of the class sections assigned to them
--     (scope class_section). Deciding a request (stu.student_elective_approvals.edit) and deleting a choice stay with
--     the principal and the registrar.
--   * principal: manages electives and decides requests, institute-wide.
INSERT INTO role_template_permissions (template_code, permission_code, scope) VALUES
    ('teacher',   'stu.student_electives.create',          'class_section'),
    ('teacher',   'stu.student_electives.edit',            'class_section'),
    ('teacher',   'stu.student_elective_approvals.create', 'class_section'),
    ('principal', 'stu.student_electives.*',               'tenant'),
    ('principal', 'stu.student_elective_approvals.*',      'tenant')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
SELECT r.tenant_id, r.id, tp.permission_code, tp.scope
FROM roles r
JOIN role_template_permissions tp ON tp.template_code = r.template_code
 AND tp.permission_code IN ('stu.student_electives.create', 'stu.student_electives.edit', 'stu.student_elective_approvals.create',
                            'stu.student_electives.*', 'stu.student_elective_approvals.*')
WHERE r.template_code IN ('teacher', 'principal') AND r.deleted_at IS NULL AND NOT r.is_system
ON CONFLICT DO NOTHING;
