-- 000005_audit: append-only audit trail and sensitive-read log (docs/plan/01-access-model.md §7).

CREATE TABLE audit_log (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     uuid REFERENCES tenants(id),
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    actor_type    text NOT NULL CHECK (actor_type IN ('user', 'platform_user', 'system', 'device')),
    actor_id      uuid,
    impersonator_id uuid,
    source        text NOT NULL CHECK (source IN ('web', 'ios', 'android', 'api', 'job', 'impersonation')),
    action        text NOT NULL,
    resource_type text NOT NULL,
    resource_id   text,
    before        jsonb,
    after         jsonb,
    request_id    text,
    ip            inet,
    user_agent    text,
    meta          jsonb NOT NULL DEFAULT '{}'::jsonb
);
COMMENT ON TABLE audit_log IS 'Immutable record of every mutation. tenant_id is NULL for platform-level actions.';
CREATE INDEX audit_log_tenant_time_idx ON audit_log (tenant_id, occurred_at DESC);
CREATE INDEX audit_log_resource_idx ON audit_log (tenant_id, resource_type, resource_id);
CREATE INDEX audit_log_actor_idx ON audit_log (tenant_id, actor_id, occurred_at DESC);
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY audit_log_tenant_read ON audit_log FOR SELECT USING (tenant_id = app_current_tenant());
CREATE POLICY audit_log_tenant_write ON audit_log FOR INSERT WITH CHECK (tenant_id = app_current_tenant());
CREATE POLICY audit_log_platform ON audit_log FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

CREATE TABLE data_access_log (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     uuid NOT NULL REFERENCES tenants(id),
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    user_id       uuid NOT NULL,
    resource_type text NOT NULL,
    resource_id   text,
    fields        text[] NOT NULL DEFAULT '{}',
    purpose       text,
    request_id    text
);
COMMENT ON TABLE data_access_log IS 'Reads of sensitive data (medical, salary, bank, discipline, documents).';
CREATE INDEX data_access_log_tenant_time_idx ON data_access_log (tenant_id, occurred_at DESC);
CREATE INDEX data_access_log_user_idx ON data_access_log (tenant_id, user_id, occurred_at DESC);
ALTER TABLE data_access_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY data_access_log_tenant_read ON data_access_log FOR SELECT USING (tenant_id = app_current_tenant());
CREATE POLICY data_access_log_tenant_write ON data_access_log FOR INSERT WITH CHECK (tenant_id = app_current_tenant());

-- Append-only: no one (not even the owner) may change or remove rows. Retention is done by dropping partitions later.
CREATE OR REPLACE FUNCTION forbid_log_mutation() RETURNS trigger
LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END
$$;
CREATE TRIGGER audit_log_immutable BEFORE UPDATE OR DELETE ON audit_log FOR EACH ROW EXECUTE FUNCTION forbid_log_mutation();
CREATE TRIGGER data_access_log_immutable BEFORE UPDATE OR DELETE ON data_access_log FOR EACH ROW EXECUTE FUNCTION forbid_log_mutation();
REVOKE UPDATE, DELETE, TRUNCATE ON audit_log, data_access_log FROM shikshit_app;
