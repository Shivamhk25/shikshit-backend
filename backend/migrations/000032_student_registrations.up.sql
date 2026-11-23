-- Student registrations (ADM-06): a family applies for a place for a child. Admission and enrolment into the students
-- register are ADM-05/07; this table keeps the application and where it stands. Personal data of a child and a parent,
-- so reads are audited. Aadhaar, religion, caste, category and income are deliberately not stored (decision 0009), and
-- neither is the APAAR ID (a government identifier; waiting for the founder's answer). Photos wait for a registration
-- photo purpose in file storage.

-- One counter per institute. A row lock on it makes concurrent registrations take turns, so two requests never get the
-- same number; the increment commits or rolls back with the registration that took it, so numbers have no gaps.
CREATE TABLE registration_counters (
    tenant_id  uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    last_value integer NOT NULL DEFAULT 0 CHECK (last_value >= 0)
);
COMMENT ON TABLE registration_counters IS 'Last application number given to a student registration, per institute (ADM-06). Another numbering helper may replace this later; the registration number format is REG-000045.';
ALTER TABLE registration_counters ENABLE ROW LEVEL SECURITY;
CREATE POLICY registration_counters_tenant ON registration_counters USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE FUNCTION next_registration_number(p_tenant uuid) RETURNS text LANGUAGE sql AS $$
    INSERT INTO registration_counters (tenant_id, last_value) VALUES (p_tenant, 1)
    ON CONFLICT (tenant_id) DO UPDATE SET last_value = registration_counters.last_value + 1
    RETURNING 'REG-' || lpad(last_value::text, 6, '0')
$$;

CREATE TABLE student_registrations (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    application_number   text NOT NULL,
    applied_on           date NOT NULL,
    first_name           text NOT NULL CHECK (length(btrim(first_name)) BETWEEN 1 AND 100),
    middle_name          text CHECK (middle_name IS NULL OR length(middle_name) <= 100),
    last_name            text CHECK (last_name IS NULL OR length(last_name) <= 100),
    date_of_birth        date NOT NULL,
    gender               text CHECK (gender IS NULL OR gender IN ('male', 'female', 'other')),
    father_name          text CHECK (father_name IS NULL OR length(father_name) <= 150),
    mother_name          text CHECK (mother_name IS NULL OR length(mother_name) <= 150),
    guardian_name        text CHECK (guardian_name IS NULL OR length(guardian_name) <= 150),
    guardian_phone       text NOT NULL CHECK (guardian_phone ~ '^[0-9+() -]{7,20}$'),
    sibling_student_code text CHECK (sibling_student_code IS NULL OR length(sibling_student_code) <= 30),
    class_id             uuid NOT NULL,
    section_id           uuid,
    year_id              uuid NOT NULL,
    status               text NOT NULL DEFAULT 'submitted' CHECK (status IN ('submitted', 'under_review', 'accepted', 'rejected', 'withdrawn')),
    enquiry_id           uuid,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    created_by           uuid,
    updated_by           uuid,
    deleted_at           timestamptz,
    FOREIGN KEY (tenant_id, class_id)   REFERENCES classes (tenant_id, id),
    FOREIGN KEY (tenant_id, section_id) REFERENCES sections (tenant_id, id),
    FOREIGN KEY (tenant_id, year_id)    REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, enquiry_id) REFERENCES enquiries (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE student_registrations IS 'A student registration (ADM-06). Personal data of a child and a parent: reads are audited. The application number is given by a trigger and never changes; a number is never reused, even after a delete.';
-- numbers are never reused, so this index covers deleted rows too
CREATE UNIQUE INDEX student_registrations_number_uq ON student_registrations (tenant_id, application_number);
-- an enquiry becomes at most one live registration
CREATE UNIQUE INDEX student_registrations_enquiry_uq ON student_registrations (tenant_id, enquiry_id) WHERE enquiry_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX student_registrations_status_idx ON student_registrations (tenant_id, status, applied_on DESC) WHERE deleted_at IS NULL;
CREATE INDEX student_registrations_class_idx ON student_registrations (tenant_id, class_id, year_id) WHERE deleted_at IS NULL;
CREATE INDEX student_registrations_phone_idx ON student_registrations (tenant_id, guardian_phone) WHERE deleted_at IS NULL;

CREATE FUNCTION student_registrations_before_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.application_number := next_registration_number(NEW.tenant_id);
    ELSE
        NEW.application_number := OLD.application_number;
        NEW.enquiry_id := OLD.enquiry_id;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER student_registrations_number BEFORE INSERT OR UPDATE ON student_registrations FOR EACH ROW EXECUTE FUNCTION student_registrations_before_write();
CREATE TRIGGER student_registrations_updated_at BEFORE UPDATE ON student_registrations FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE student_registrations ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_registrations_tenant ON student_registrations USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
