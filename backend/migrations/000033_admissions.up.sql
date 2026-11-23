-- Admissions (ADM-05): an accepted registration becomes a student. The registration keeps a link to the student it
-- became, so a second admit finds the same student (one registration, one student, always). The registration's status
-- is left as it is ("accepted"): admitted is a fact about the registration, not another stage of the application, and
-- the registrations screen, its filters and ADM-06 stay as they were. "Admitted" for the funnel is
-- admitted_student_id IS NOT NULL.

ALTER TABLE student_registrations
    ADD COLUMN admitted_student_id uuid,
    ADD COLUMN admitted_at         timestamptz,
    ADD COLUMN admitted_by         uuid,
    ADD CONSTRAINT student_registrations_admitted_student_fk FOREIGN KEY (tenant_id, admitted_student_id) REFERENCES students (tenant_id, id),
    ADD CONSTRAINT student_registrations_admitted_pair_ck CHECK ((admitted_student_id IS NULL) = (admitted_at IS NULL));
COMMENT ON COLUMN student_registrations.admitted_student_id IS 'The student this registration became (ADM-05). Set once, by the admit action; never changed or cleared afterwards.';

-- a student comes from at most one registration, and a registration becomes at most one student
CREATE UNIQUE INDEX student_registrations_admitted_uq ON student_registrations (tenant_id, admitted_student_id) WHERE admitted_student_id IS NOT NULL;
-- the queue: accepted, not yet admitted
CREATE INDEX student_registrations_queue_idx ON student_registrations (tenant_id, year_id, class_id, applied_on)
    WHERE status = 'accepted' AND admitted_student_id IS NULL AND deleted_at IS NULL;

-- the admission, once made, cannot be undone or pointed at another student by an ordinary update
CREATE OR REPLACE FUNCTION student_registrations_before_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.application_number := next_registration_number(NEW.tenant_id);
        NEW.admitted_student_id := NULL;
        NEW.admitted_at := NULL;
        NEW.admitted_by := NULL;
    ELSE
        NEW.application_number := OLD.application_number;
        NEW.enquiry_id := OLD.enquiry_id;
        IF OLD.admitted_student_id IS NOT NULL THEN
            NEW.admitted_student_id := OLD.admitted_student_id;
            NEW.admitted_at := OLD.admitted_at;
            NEW.admitted_by := OLD.admitted_by;
        END IF;
    END IF;
    RETURN NEW;
END $$;

-- Automatic student codes (STU-000045), for an admission where the school does not type a code (decision 0009 lets the
-- school type codes; the admit form accepts one). Same shape as REG- and ENQ-: one counter per institute, locked by the
-- row, committed with the admission. A number whose code a school already typed by hand is skipped, never reused.
CREATE TABLE student_code_counters (
    tenant_id  uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    last_value integer NOT NULL DEFAULT 0 CHECK (last_value >= 0)
);
COMMENT ON TABLE student_code_counters IS 'Last automatic student code number, per institute (ADM-05). The code format is STU-000045.';
ALTER TABLE student_code_counters ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_code_counters_tenant ON student_code_counters USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE FUNCTION next_student_code(p_tenant uuid) RETURNS text LANGUAGE plpgsql AS $$
DECLARE
    n    integer;
    code text;
BEGIN
    LOOP
        INSERT INTO student_code_counters (tenant_id, last_value) VALUES (p_tenant, 1)
        ON CONFLICT (tenant_id) DO UPDATE SET last_value = student_code_counters.last_value + 1
        RETURNING last_value INTO n;
        code := 'STU-' || lpad(n::text, 6, '0');
        -- deleted students count too: a code is not given out again
        EXIT WHEN NOT EXISTS (SELECT 1 FROM students WHERE tenant_id = p_tenant AND lower(registration_code) = lower(code));
    END LOOP;
    RETURN code;
END $$;
