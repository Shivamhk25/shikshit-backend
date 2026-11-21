-- Guardians, their links to students, and student addresses (STU-02, STU-11). A guardian is a person who can be linked
-- to several students (siblings), so the person and the link are separate rows. Occupation, income and other sensitive
-- attributes are deliberately not stored; see docs/engineering/students-design.md.

CREATE TABLE guardians (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 150),
    phone      text NOT NULL CHECK (phone ~ '^[0-9+() -]{7,20}$'),
    email      text CHECK (email IS NULL OR length(email) <= 254),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    updated_by uuid,
    deleted_at timestamptz,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE guardians IS 'A parent or guardian (STU-02). Personal data: reads are audited. One person is one row however many students they are linked to; two people may share a phone, so identity is phone plus name.';
CREATE UNIQUE INDEX guardians_phone_name_uq ON guardians (tenant_id, phone, lower(btrim(name))) WHERE deleted_at IS NULL;
CREATE INDEX guardians_phone_idx ON guardians (tenant_id, phone) WHERE deleted_at IS NULL;
CREATE TRIGGER guardians_updated_at BEFORE UPDATE ON guardians FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE guardians ENABLE ROW LEVEL SECURITY;
CREATE POLICY guardians_tenant ON guardians USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE student_guardians (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    student_id       uuid NOT NULL,
    guardian_id      uuid NOT NULL,
    relation         text NOT NULL CHECK (relation IN ('father', 'mother', 'guardian', 'grandparent', 'sibling', 'other')),
    is_primary       boolean NOT NULL DEFAULT false,
    is_legal_custody boolean NOT NULL DEFAULT false,
    can_pickup       boolean NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    created_by       uuid,
    updated_by       uuid,
    deleted_at       timestamptz,
    FOREIGN KEY (tenant_id, student_id)  REFERENCES students (tenant_id, id),
    FOREIGN KEY (tenant_id, guardian_id) REFERENCES guardians (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE student_guardians IS 'Links a guardian to a student (STU-02). A student has at most one primary guardian.';
CREATE UNIQUE INDEX student_guardians_pair_uq ON student_guardians (tenant_id, student_id, guardian_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX student_guardians_primary_uq ON student_guardians (tenant_id, student_id) WHERE is_primary AND deleted_at IS NULL;
CREATE INDEX student_guardians_guardian_idx ON student_guardians (tenant_id, guardian_id) WHERE deleted_at IS NULL;
CREATE TRIGGER student_guardians_updated_at BEFORE UPDATE ON student_guardians FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE student_guardians ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_guardians_tenant ON student_guardians USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE student_addresses (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    student_id uuid NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('home', 'mailing')),
    line1      text NOT NULL CHECK (length(btrim(line1)) BETWEEN 1 AND 200),
    line2      text CHECK (line2 IS NULL OR length(line2) <= 200),
    city       text NOT NULL CHECK (length(btrim(city)) BETWEEN 1 AND 100),
    state      text CHECK (state IS NULL OR length(state) <= 100),
    postcode   text CHECK (postcode IS NULL OR postcode ~ '^[A-Za-z0-9 -]{3,10}$'),
    country    text NOT NULL DEFAULT 'India' CHECK (length(btrim(country)) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    updated_by uuid,
    deleted_at timestamptz,
    FOREIGN KEY (tenant_id, student_id) REFERENCES students (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE student_addresses IS 'A student''s home or mailing address (STU-11). One of each kind per student. Personal data: reads are audited. No geocoding.';
CREATE UNIQUE INDEX student_addresses_kind_uq ON student_addresses (tenant_id, student_id, kind) WHERE deleted_at IS NULL;
CREATE TRIGGER student_addresses_updated_at BEFORE UPDATE ON student_addresses FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE student_addresses ENABLE ROW LEVEL SECURITY;
CREATE POLICY student_addresses_tenant ON student_addresses USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
