-- 000002_tenancy: plans, tenants (institutes) and module entitlements.

CREATE TABLE plans (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text NOT NULL UNIQUE CHECK (code ~ '^[a-z0-9_-]+$'),
    name       text NOT NULL,
    modules    text[] NOT NULL DEFAULT '{}',
    limits     jsonb NOT NULL DEFAULT '{}'::jsonb,
    is_active  boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE plans IS 'Commercial plans; modules lists the module codes included. Platform-managed.';
CREATE TRIGGER plans_updated_at BEFORE UPDATE ON plans FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE plans ENABLE ROW LEVEL SECURITY;
CREATE POLICY plans_platform ON plans FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

CREATE TABLE tenants (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code           citext NOT NULL UNIQUE CHECK (code ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    name           text NOT NULL,
    custom_domain  citext UNIQUE,
    status         text NOT NULL DEFAULT 'onboarding' CHECK (status IN ('onboarding', 'live', 'suspended', 'archived')),
    plan_id        uuid REFERENCES plans(id),
    timezone       text NOT NULL DEFAULT 'Asia/Kolkata',
    locale         text NOT NULL DEFAULT 'en-IN',
    currency       char(3) NOT NULL DEFAULT 'INR',
    brand          jsonb NOT NULL DEFAULT '{}'::jsonb,
    settings       jsonb NOT NULL DEFAULT '{}'::jsonb,
    max_users      integer CHECK (max_users IS NULL OR max_users > 0),
    max_storage_mb integer CHECK (max_storage_mb IS NULL OR max_storage_mb > 0),
    went_live_at   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz
);
COMMENT ON TABLE tenants IS 'Institutes. status: onboarding -> live (ONB-23), suspended, archived.';
CREATE TRIGGER tenants_updated_at BEFORE UPDATE ON tenants FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
-- A tenant session sees only its own institute; platform sessions manage all.
CREATE POLICY tenants_self ON tenants FOR SELECT USING (id = app_current_tenant());
CREATE POLICY tenants_platform ON tenants FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

CREATE TABLE tenant_modules (
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    module_code text NOT NULL CHECK (module_code ~ '^[a-z]{2,8}$'),
    enabled     boolean NOT NULL DEFAULT true,
    enabled_at  timestamptz NOT NULL DEFAULT now(),
    enabled_by  uuid,
    PRIMARY KEY (tenant_id, module_code)
);
COMMENT ON TABLE tenant_modules IS 'Module entitlements per institute (Super Admin). Disabled modules vanish from navigation and return 404.';
ALTER TABLE tenant_modules ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_modules_self ON tenant_modules FOR SELECT USING (tenant_id = app_current_tenant());
CREATE POLICY tenant_modules_platform ON tenant_modules FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

-- Pre-login tenant resolution (host -> institute). Row-level security hides tenants from anonymous sessions,
-- so this narrow SECURITY DEFINER function returns only the public branding fields.
CREATE OR REPLACE FUNCTION resolve_tenant(p_key text)
RETURNS TABLE (id uuid, code citext, name text, status text, brand jsonb)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS
$$
    SELECT t.id, t.code, t.name, t.status, t.brand
    FROM tenants t
    WHERE t.deleted_at IS NULL AND (t.code = p_key::citext OR t.custom_domain = p_key::citext)
    LIMIT 1
$$;
REVOKE ALL ON FUNCTION resolve_tenant(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_tenant(text) TO shikshit_app;
