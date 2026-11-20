-- 000010_academic_masters_batch1: ACA-18 School Divisions, ACA-11 Campuses, ACA-17 Holiday Calendars,
-- ACA-29 Academic Calendars (events), ACA-10 Elective Groups; link classes to divisions; constrain the subject
-- category and area lists. Same pattern as 000008/000009.

CREATE TABLE school_divisions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code          text NOT NULL CHECK (length(btrim(code)) BETWEEN 1 AND 20),
    name          text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    display_order integer NOT NULL DEFAULT 0 CHECK (display_order BETWEEN 0 AND 100000),
    description   text CHECK (description IS NULL OR length(description) <= 500),
    status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    created_by    uuid,
    updated_by    uuid,
    deleted_at    timestamptz,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE school_divisions IS 'Divisions of a school such as Primary or Senior (ACA-18); classes may belong to one.';
CREATE UNIQUE INDEX school_divisions_tenant_code_uq ON school_divisions (tenant_id, lower(code)) WHERE deleted_at IS NULL;
CREATE INDEX school_divisions_tenant_order_idx ON school_divisions (tenant_id, display_order, name) WHERE deleted_at IS NULL;
CREATE TRIGGER school_divisions_updated_at BEFORE UPDATE ON school_divisions FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE school_divisions ENABLE ROW LEVEL SECURITY;
CREATE POLICY school_divisions_tenant ON school_divisions USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

ALTER TABLE classes ADD COLUMN division_id uuid;
ALTER TABLE classes ADD CONSTRAINT classes_division_fk FOREIGN KEY (tenant_id, division_id) REFERENCES school_divisions (tenant_id, id);
CREATE INDEX classes_division_idx ON classes (tenant_id, division_id) WHERE deleted_at IS NULL AND division_id IS NOT NULL;

CREATE TABLE campuses (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code              text CHECK (code IS NULL OR length(code) BETWEEN 1 AND 20),
    name              text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    check_in_radius_m integer NOT NULL CHECK (check_in_radius_m BETWEEN 10 AND 5000),
    latitude          numeric(9, 6) CHECK (latitude BETWEEN -90 AND 90),
    longitude         numeric(9, 6) CHECK (longitude BETWEEN -180 AND 180),
    staff_due_in      text CHECK (staff_due_in IS NULL OR staff_due_in ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    address           text CHECK (address IS NULL OR length(address) <= 300),
    remarks           text CHECK (remarks IS NULL OR length(remarks) <= 500),
    status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    created_by        uuid,
    updated_by        uuid,
    deleted_at        timestamptz,
    CHECK ((latitude IS NULL) = (longitude IS NULL)),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE campuses IS 'Campuses with an optional map position and staff check-in circle (ACA-11).';
CREATE UNIQUE INDEX campuses_tenant_code_uq ON campuses (tenant_id, lower(code)) WHERE code IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX campuses_tenant_name_idx ON campuses (tenant_id, name) WHERE deleted_at IS NULL;
CREATE TRIGGER campuses_updated_at BEFORE UPDATE ON campuses FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE campuses ENABLE ROW LEVEL SECURITY;
CREATE POLICY campuses_tenant ON campuses USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE holidays (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    year_id             uuid NOT NULL,
    title               text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 100),
    holiday_type        text NOT NULL CHECK (holiday_type IN ('public', 'school', 'regional', 'restricted', 'other')),
    start_date          date NOT NULL,
    end_date            date NOT NULL,
    is_optional         boolean NOT NULL DEFAULT false,
    is_recurring_annual boolean NOT NULL DEFAULT false,
    applicable_for      text CHECK (applicable_for IS NULL OR length(applicable_for) <= 100),
    description         text CHECK (description IS NULL OR length(description) <= 500),
    status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    created_by          uuid,
    updated_by          uuid,
    deleted_at          timestamptz,
    CHECK (end_date >= start_date),
    FOREIGN KEY (tenant_id, year_id) REFERENCES academic_years (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE holidays IS 'Holidays of an academic year (ACA-17).';
CREATE INDEX holidays_year_idx ON holidays (tenant_id, year_id, start_date) WHERE deleted_at IS NULL;
CREATE TRIGGER holidays_updated_at BEFORE UPDATE ON holidays FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE holidays ENABLE ROW LEVEL SECURITY;
CREATE POLICY holidays_tenant ON holidays USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE calendar_events (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    year_id              uuid NOT NULL,
    title                text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 100),
    event_type           text NOT NULL CHECK (event_type IN ('academic', 'exam', 'event', 'meeting', 'activity', 'other')),
    start_date           date NOT NULL,
    end_date             date NOT NULL,
    is_all_day           boolean NOT NULL DEFAULT true,
    is_instructional_day boolean NOT NULL DEFAULT false,
    is_examination_day   boolean NOT NULL DEFAULT false,
    venue                text CHECK (venue IS NULL OR length(venue) <= 100),
    description          text CHECK (description IS NULL OR length(description) <= 500),
    status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    created_by           uuid,
    updated_by           uuid,
    deleted_at           timestamptz,
    CHECK (end_date >= start_date),
    FOREIGN KEY (tenant_id, year_id) REFERENCES academic_years (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE calendar_events IS 'Events of the academic calendar of a year (ACA-29).';
CREATE INDEX calendar_events_year_idx ON calendar_events (tenant_id, year_id, start_date) WHERE deleted_at IS NULL;
CREATE TRIGGER calendar_events_updated_at BEFORE UPDATE ON calendar_events FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE calendar_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY calendar_events_tenant ON calendar_events USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE elective_groups (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code          text CHECK (code IS NULL OR length(code) BETWEEN 1 AND 20),
    name          text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    min_subjects  integer NOT NULL CHECK (min_subjects BETWEEN 0 AND 50),
    max_subjects  integer NOT NULL CHECK (max_subjects BETWEEN 1 AND 50),
    enforce_limits boolean NOT NULL DEFAULT true,
    display_order integer NOT NULL DEFAULT 0 CHECK (display_order BETWEEN 0 AND 100000),
    description   text CHECK (description IS NULL OR length(description) <= 500),
    status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    created_by    uuid,
    updated_by    uuid,
    deleted_at    timestamptz,
    CHECK (min_subjects <= max_subjects),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE elective_groups IS 'Groups of elective subjects with how many a student must or may take (ACA-10). Subject membership arrives with ACA-31.';
CREATE UNIQUE INDEX elective_groups_tenant_code_uq ON elective_groups (tenant_id, lower(code)) WHERE code IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX elective_groups_tenant_order_idx ON elective_groups (tenant_id, display_order, name) WHERE deleted_at IS NULL;
CREATE TRIGGER elective_groups_updated_at BEFORE UPDATE ON elective_groups FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE elective_groups ENABLE ROW LEVEL SECURITY;
CREATE POLICY elective_groups_tenant ON elective_groups USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

-- Recommended lists for the subject category and area (product may extend them with a later migration).
-- NOT VALID: new and changed rows must conform; rows written before this migration are not re-checked.
ALTER TABLE subjects ADD CONSTRAINT subjects_category_chk CHECK (category IN ('core', 'elective', 'language', 'optional', 'vocational')) NOT VALID;
ALTER TABLE subjects ADD CONSTRAINT subjects_area_chk CHECK (area IN ('languages', 'mathematics', 'science', 'social_science', 'arts', 'physical_education', 'technology', 'vocational', 'life_skills', 'other')) NOT VALID;
