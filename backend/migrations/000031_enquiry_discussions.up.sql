-- Enquiry discussions (ADM-04) and automatic enquiry numbers (A-20).
--
-- 1. Enquiries are numbered by the database: ENQ-000001, ENQ-000002, ... per institute. The number comes from a
--    per-institute counter that is incremented inside the creating transaction, so two enquiries created at the same
--    moment can never get the same number (the second waits for the first). Numbers only go up. A rolled-back create
--    gives its number back; a deleted enquiry keeps its number, so a number is never used for two enquiries.
--    Enquiries created before this migration keep the code the school typed. If a typed code looks like an
--    automatic one (for example ENQ-000007 typed by hand), the counter skips to the next free number.
-- 2. A discussion is one follow-up contact with a family about an enquiry (a call, a visit, a WhatsApp message).
--    It holds what a parent said about a child, so it is personal data like the enquiry itself: reads are audited.

CREATE TABLE enquiry_counters (
    tenant_id  uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    last_value bigint NOT NULL CHECK (last_value > 0)
);
COMMENT ON TABLE enquiry_counters IS 'Last automatic enquiry number given in each institute (A-20). Written only by enquiries_assign_code().';
ALTER TABLE enquiry_counters ENABLE ROW LEVEL SECURITY;
CREATE POLICY enquiry_counters_tenant ON enquiry_counters USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE FUNCTION enquiries_assign_code() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    n bigint;
    candidate text;
BEGIN
    IF NEW.code IS NOT NULL AND btrim(NEW.code) <> '' THEN
        RETURN NEW;
    END IF;
    LOOP
        -- the upsert takes a row lock held until the creating transaction ends: concurrent creates take turns
        INSERT INTO enquiry_counters (tenant_id, last_value) VALUES (NEW.tenant_id, 1)
        ON CONFLICT (tenant_id) DO UPDATE SET last_value = enquiry_counters.last_value + 1
        RETURNING last_value INTO n;
        candidate := 'ENQ-' || lpad(n::text, 6, '0');
        EXIT WHEN NOT EXISTS (SELECT 1 FROM enquiries WHERE tenant_id = NEW.tenant_id AND lower(code) = lower(candidate) AND deleted_at IS NULL);
    END LOOP;
    NEW.code := candidate;
    RETURN NEW;
END;
$$;
CREATE TRIGGER enquiries_assign_code BEFORE INSERT ON enquiries FOR EACH ROW EXECUTE FUNCTION enquiries_assign_code();
COMMENT ON TABLE enquiries IS 'An admission enquiry (ADM-03). Personal data of a child and a parent: reads are audited. Codes are numbered by the database (ENQ-000123, A-20); rows from before keep the code the school typed. Status "converted" is set by hand until ADM-06 links an enquiry to a registration.';

CREATE TABLE enquiry_discussions (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    enquiry_id          uuid NOT NULL,
    contacted_at        timestamptz NOT NULL,
    channel             text NOT NULL CHECK (channel IN ('call', 'visit', 'whatsapp', 'email', 'other')),
    summary             text NOT NULL CHECK (length(btrim(summary)) BETWEEN 1 AND 200),
    notes               text CHECK (notes IS NULL OR length(notes) <= 2000),
    outcome             text NOT NULL CHECK (outcome IN ('interested', 'visit_scheduled', 'call_back_later', 'not_reachable', 'not_interested', 'other')),
    next_follow_up_date date,
    language            text NOT NULL CHECK (language IN ('en', 'hi', 'bn', 'mr', 'te', 'ta', 'gu', 'ur', 'kn', 'ml', 'or', 'pa', 'as', 'other')),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    created_by          uuid,
    updated_by          uuid,
    deleted_at          timestamptz,
    FOREIGN KEY (tenant_id, enquiry_id) REFERENCES enquiries (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE enquiry_discussions IS 'One follow-up contact with a family about an enquiry (ADM-04). Personal data: reads are audited. created_by is who logged it.';
CREATE INDEX enquiry_discussions_enquiry_idx ON enquiry_discussions (tenant_id, enquiry_id, contacted_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX enquiry_discussions_contacted_idx ON enquiry_discussions (tenant_id, contacted_at DESC) WHERE deleted_at IS NULL;
CREATE TRIGGER enquiry_discussions_updated_at BEFORE UPDATE ON enquiry_discussions FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE enquiry_discussions ENABLE ROW LEVEL SECURITY;
CREATE POLICY enquiry_discussions_tenant ON enquiry_discussions USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
