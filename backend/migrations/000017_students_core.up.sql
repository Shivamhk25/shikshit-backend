-- Students core (STU-02): the student's identity, and one enrollment per academic year that places the student in a
-- class and section. Institute-scoped with row-level security and soft deletes. Photos, documents and the sensitive
-- attributes (religion, category, income, visa) are deliberately not here; see docs/engineering/students-design.md.

CREATE TABLE students (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    registration_code text NOT NULL CHECK (length(btrim(registration_code)) BETWEEN 1 AND 30),
    first_name        text NOT NULL CHECK (length(btrim(first_name)) BETWEEN 1 AND 100),
    middle_name       text CHECK (middle_name IS NULL OR length(middle_name) <= 100),
    last_name         text CHECK (last_name IS NULL OR length(last_name) <= 100),
    gender            text NOT NULL CHECK (gender IN ('male', 'female', 'other')),
    date_of_birth     date NOT NULL CHECK (date_of_birth >= DATE '1950-01-01'),
    admission_date    date,
    mobile            text CHECK (mobile IS NULL OR mobile ~ '^[0-9+() -]{7,20}$'),
    parent_email      text CHECK (parent_email IS NULL OR length(parent_email) <= 254),
    student_email     text CHECK (student_email IS NULL OR length(student_email) <= 254),
    apaar_id          text CHECK (apaar_id IS NULL OR apaar_id ~ '^[0-9]{12}$'),
    status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'transferred', 'alumni')),
    remarks           text CHECK (remarks IS NULL OR length(remarks) <= 500),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    created_by        uuid,
    updated_by        uuid,
    deleted_at        timestamptz,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE students IS 'A student''s identity (STU-02). Class and section come from the enrollment of the academic year, not from this row. Personal data of minors: read access to details is audited.';
CREATE UNIQUE INDEX students_tenant_code_uq ON students (tenant_id, lower(registration_code)) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX students_tenant_apaar_uq ON students (tenant_id, apaar_id) WHERE apaar_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX students_name_idx ON students (tenant_id, lower(first_name), lower(last_name)) WHERE deleted_at IS NULL;
CREATE TRIGGER students_updated_at BEFORE UPDATE ON students FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE students ENABLE ROW LEVEL SECURITY;
CREATE POLICY students_tenant ON students USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE student_enrollments (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    student_id        uuid NOT NULL,
    year_id           uuid NOT NULL,
    class_id          uuid NOT NULL,
    section_id        uuid NOT NULL,
    roll_number       text CHECK (roll_number IS NULL OR length(btrim(roll_number)) BETWEEN 1 AND 20),
    enrollment_number text CHECK (enrollment_number IS NULL OR length(btrim(enrollment_number)) BETWEEN 1 AND 30),
    enrollment_date   date,
    status            text NOT NULL DEFAULT 'enrolled' CHECK (status IN ('enrolled', 'withdrawn', 'completed')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    created_by        uuid,
    updated_by        uuid,
    deleted_at        timestamptz,
    FOREIGN KEY (tenant_id, student_id) REFERENCES students (tenant_id, id),
    FOREIGN KEY (tenant_id, year_id)    REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, class_id)   REFERENCES classes (tenant_id, id),
    FOREIGN KEY (tenant_id, section_id) REFERENCES sections (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE student_enrollments IS 'Places a student in a class and section for one academic year (STU-02). The section must be mapped to the class that year (checked by the service).';
CREATE UNIQUE INDEX student_enrollments_student_year_uq ON student_enrollments (tenant_id, student_id, year_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX student_enrollments_roll_uq ON student_enrollments (tenant_id, year_id, class_id, section_id, lower(roll_number)) WHERE roll_number IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX student_enrollments_number_uq ON student_enrollments (tenant_id, lower(enrollment_number)) WHERE enrollment_number IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX student_enrollments_class_idx ON student_enrollments (tenant_id, year_id, class_id, section_id) WHERE deleted_at IS NULL;
CREATE TRIGGER student_enrollments_updated_at BEFORE UPDATE ON student_enrollments FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE student_enrollments ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_enrollments_tenant ON student_enrollments USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
