-- 000009_academic_structure: ACA-15 Academic Years, ACA-16 Academic Terms, ACA-20 Classes, ACA-21 Sections, ACA-22 Subjects.
-- Same pattern as rooms (000008): tenant_id + RLS, soft delete, audit columns, codes unique among live rows.
-- Date rules that need to look at other rows (no overlapping years, terms inside their year) are enforced by the
-- service inside the write transaction under an advisory lock; the CHECKs below hold what one row can guarantee.

CREATE TABLE academic_years (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code       text NOT NULL CHECK (length(btrim(code)) BETWEEN 1 AND 20),
    name       text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    start_date date NOT NULL,
    end_date   date NOT NULL,
    is_current boolean NOT NULL DEFAULT false,
    remarks    text CHECK (remarks IS NULL OR length(remarks) <= 500),
    status     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    updated_by uuid,
    deleted_at timestamptz,
    CHECK (end_date > start_date),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE academic_years IS 'Academic years (ACA-15). At most one live year per institute is current.';
CREATE UNIQUE INDEX academic_years_tenant_code_uq ON academic_years (tenant_id, lower(code)) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX academic_years_one_current_uq ON academic_years (tenant_id) WHERE is_current AND deleted_at IS NULL;
CREATE INDEX academic_years_tenant_start_idx ON academic_years (tenant_id, start_date DESC) WHERE deleted_at IS NULL;
CREATE TRIGGER academic_years_updated_at BEFORE UPDATE ON academic_years FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE academic_years ENABLE ROW LEVEL SECURITY;
CREATE POLICY academic_years_tenant ON academic_years USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE academic_terms (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    year_id    uuid NOT NULL,
    code       text NOT NULL CHECK (length(btrim(code)) BETWEEN 1 AND 20),
    title      text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 100),
    start_date date NOT NULL,
    end_date   date NOT NULL,
    color      text CHECK (color IS NULL OR color ~ '^#[0-9A-Fa-f]{6}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    updated_by uuid,
    deleted_at timestamptz,
    CHECK (end_date >= start_date),
    FOREIGN KEY (tenant_id, year_id) REFERENCES academic_years (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE academic_terms IS 'Terms of an academic year (ACA-16). Dates must lie inside the year and not overlap another term of it (service rule).';
CREATE UNIQUE INDEX academic_terms_year_code_uq ON academic_terms (tenant_id, year_id, lower(code)) WHERE deleted_at IS NULL;
CREATE INDEX academic_terms_year_idx ON academic_terms (tenant_id, year_id, start_date) WHERE deleted_at IS NULL;
CREATE TRIGGER academic_terms_updated_at BEFORE UPDATE ON academic_terms FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE academic_terms ENABLE ROW LEVEL SECURITY;
CREATE POLICY academic_terms_tenant ON academic_terms USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE classes (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code                text NOT NULL CHECK (length(btrim(code)) BETWEEN 1 AND 20),
    name                text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    display_order       integer NOT NULL DEFAULT 0 CHECK (display_order BETWEEN 0 AND 100000),
    youngest_age_months integer CHECK (youngest_age_months IS NULL OR youngest_age_months BETWEEN 0 AND 600),
    oldest_age_months   integer CHECK (oldest_age_months IS NULL OR oldest_age_months BETWEEN 0 AND 600),
    description         text CHECK (description IS NULL OR length(description) <= 500),
    status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    created_by          uuid,
    updated_by          uuid,
    deleted_at          timestamptz,
    CHECK (youngest_age_months IS NULL OR oldest_age_months IS NULL OR youngest_age_months <= oldest_age_months),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE classes IS 'Classes / grades such as "Class 5" (ACA-20). The school-division link arrives with ACA-18.';
CREATE UNIQUE INDEX classes_tenant_code_uq ON classes (tenant_id, lower(code)) WHERE deleted_at IS NULL;
CREATE INDEX classes_tenant_order_idx ON classes (tenant_id, display_order, name) WHERE deleted_at IS NULL;
CREATE TRIGGER classes_updated_at BEFORE UPDATE ON classes FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE classes ENABLE ROW LEVEL SECURITY;
CREATE POLICY classes_tenant ON classes USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE sections (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code        text NOT NULL CHECK (length(btrim(code)) BETWEEN 1 AND 20),
    name        text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    short_name  text CHECK (short_name IS NULL OR length(short_name) <= 20),
    description text CHECK (description IS NULL OR length(description) <= 500),
    status      text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid,
    updated_by  uuid,
    deleted_at  timestamptz,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE sections IS 'Section labels such as A, B (ACA-21). Which sections a class has is the class-section mapping (ACA-30).';
CREATE UNIQUE INDEX sections_tenant_code_uq ON sections (tenant_id, lower(code)) WHERE deleted_at IS NULL;
CREATE INDEX sections_tenant_name_idx ON sections (tenant_id, name) WHERE deleted_at IS NULL;
CREATE TRIGGER sections_updated_at BEFORE UPDATE ON sections FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE sections ENABLE ROW LEVEL SECURITY;
CREATE POLICY sections_tenant ON sections USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE subjects (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code           text NOT NULL CHECK (length(btrim(code)) BETWEEN 1 AND 20),
    name           text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    short_name     text CHECK (short_name IS NULL OR length(short_name) <= 20),
    color          text CHECK (color IS NULL OR color ~ '^#[0-9A-Fa-f]{6}$'),
    category       text NOT NULL CHECK (length(btrim(category)) BETWEEN 1 AND 50),
    area           text NOT NULL CHECK (length(btrim(area)) BETWEEN 1 AND 50),
    scholastic     boolean NOT NULL DEFAULT false,
    co_scholastic  boolean NOT NULL DEFAULT false,
    non_scholastic boolean NOT NULL DEFAULT false,
    credits        numeric(4, 1) NOT NULL CHECK (credits BETWEEN 0 AND 99.9),
    description    text CHECK (description IS NULL OR length(description) <= 500),
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    created_by     uuid,
    updated_by     uuid,
    deleted_at     timestamptz,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE subjects IS 'Subjects (ACA-22). Category and area are free text until product confirms the lists (open question in the ACA spec).';
CREATE UNIQUE INDEX subjects_tenant_code_uq ON subjects (tenant_id, lower(code)) WHERE deleted_at IS NULL;
CREATE INDEX subjects_tenant_name_idx ON subjects (tenant_id, name) WHERE deleted_at IS NULL;
CREATE TRIGGER subjects_updated_at BEFORE UPDATE ON subjects FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE subjects ENABLE ROW LEVEL SECURITY;
CREATE POLICY subjects_tenant ON subjects USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
