DROP FUNCTION IF EXISTS next_student_code(uuid);
DROP TABLE IF EXISTS student_code_counters;
CREATE OR REPLACE FUNCTION student_registrations_before_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.application_number := next_registration_number(NEW.tenant_id);
    ELSE
        NEW.application_number := OLD.application_number;
        NEW.enquiry_id := OLD.enquiry_id;
    END IF;
    RETURN NEW;
END $$;
DROP INDEX IF EXISTS student_registrations_queue_idx;
DROP INDEX IF EXISTS student_registrations_admitted_uq;
ALTER TABLE student_registrations DROP CONSTRAINT IF EXISTS student_registrations_admitted_pair_ck, DROP CONSTRAINT IF EXISTS student_registrations_admitted_student_fk,
    DROP COLUMN IF EXISTS admitted_by, DROP COLUMN IF EXISTS admitted_at, DROP COLUMN IF EXISTS admitted_student_id;
