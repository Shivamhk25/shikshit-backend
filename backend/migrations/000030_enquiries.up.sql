-- Admission enquiries (ADM-03): a parent asks about a place for a child. Converting an enquiry to a registration is
-- ADM-06; this table only keeps the enquiry and where it stands. Personal data of a child and a parent, so reads are
-- audited. Aadhaar, religion, caste and income are deliberately not stored (decision 0009).

CREATE TABLE enquiries (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code          text NOT NULL CHECK (length(btrim(code)) BETWEEN 1 AND 20),
    enquirer_name text NOT NULL CHECK (length(btrim(enquirer_name)) BETWEEN 1 AND 150),
    phone         text NOT NULL CHECK (phone ~ '^[0-9+() -]{7,20}$'),
    email         text CHECK (email IS NULL OR length(email) <= 254),
    child_name    text NOT NULL CHECK (length(btrim(child_name)) BETWEEN 1 AND 150),
    date_of_birth date,
    class_id      uuid,
    year_id       uuid,
    source        text NOT NULL CHECK (source IN ('walk_in', 'phone', 'website', 'referral', 'social', 'other')),
    status        text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'contacted', 'visit_scheduled', 'converted', 'closed')),
    follow_up_date date,
    notes         text CHECK (notes IS NULL OR length(notes) <= 1000),
    assigned_to   uuid,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    created_by    uuid,
    updated_by    uuid,
    deleted_at    timestamptz,
    FOREIGN KEY (tenant_id, class_id)    REFERENCES classes (tenant_id, id),
    FOREIGN KEY (tenant_id, year_id)     REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, assigned_to) REFERENCES users (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE enquiries IS 'An admission enquiry (ADM-03). Personal data of a child and a parent: reads are audited. Codes are typed by the school. Status "converted" is set by hand until ADM-06 links an enquiry to a registration.';
CREATE UNIQUE INDEX enquiries_tenant_code_uq ON enquiries (tenant_id, lower(code)) WHERE deleted_at IS NULL;
CREATE INDEX enquiries_phone_idx ON enquiries (tenant_id, phone) WHERE deleted_at IS NULL;
CREATE INDEX enquiries_status_idx ON enquiries (tenant_id, status, follow_up_date) WHERE deleted_at IS NULL;
CREATE INDEX enquiries_class_idx ON enquiries (tenant_id, class_id) WHERE deleted_at IS NULL;
CREATE TRIGGER enquiries_updated_at BEFORE UPDATE ON enquiries FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE enquiries ENABLE ROW LEVEL SECURITY;
CREATE POLICY enquiries_tenant ON enquiries USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
