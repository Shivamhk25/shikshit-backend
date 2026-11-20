-- 000008_rooms: ACA-24 Rooms, the pilot resource for the CRUD kit (docs/plan/modules/ACA-academic-setup-scheduling.md).
-- Pattern every master-data table follows: tenant_id + row-level security, soft delete, created/updated by, and a
-- unique code that is freed when the row is soft-deleted.

CREATE TABLE rooms (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id              uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code                   text CHECK (code IS NULL OR length(code) BETWEEN 1 AND 20),
    name                   text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    room_type              text CHECK (room_type IN ('classroom', 'lab', 'library', 'hall', 'staff', 'other')),
    capacity               integer NOT NULL CHECK (capacity BETWEEN 1 AND 10000),
    building               text CHECK (building IS NULL OR length(building) <= 100),
    floor                  text CHECK (floor IS NULL OR length(floor) <= 20),
    available_for_timetable boolean NOT NULL DEFAULT true,
    display_order          integer NOT NULL DEFAULT 0 CHECK (display_order BETWEEN 0 AND 100000),
    description            text CHECK (description IS NULL OR length(description) <= 500),
    status                 text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    created_by             uuid,
    updated_by             uuid,
    deleted_at             timestamptz,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE rooms IS 'Physical rooms of an institute (ACA-24). Soft-deleted; the code is unique among live rooms only.';
CREATE UNIQUE INDEX rooms_tenant_code_uq ON rooms (tenant_id, lower(code)) WHERE code IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX rooms_tenant_order_idx ON rooms (tenant_id, display_order, name) WHERE deleted_at IS NULL;
CREATE TRIGGER rooms_updated_at BEFORE UPDATE ON rooms FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE rooms ENABLE ROW LEVEL SECURITY;
CREATE POLICY rooms_tenant ON rooms USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
