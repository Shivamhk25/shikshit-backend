-- Onboarding control tower (ONB-01): one plan per institute and one row per onboarding domain holding what people decide
-- (owner, due date, whether the owner has asked for review and whether the Admin has confirmed it ready). What the data
-- looks like (counts, coverage) is never stored: it is computed from the tables when the tower is read, so it cannot
-- drift from the data. Rows are created on first use.
--
-- Adds the edit permission to onb.control_tower (view already exists) and gives the Principal the whole resource, as the
-- mobile spec has the Principal assign and nudge. The Admin holds it through its *.*.* grant.

INSERT INTO permissions (code, module, resource, action, sensitive)
VALUES ('onb.control_tower.edit', 'onb', 'control_tower', 'edit', false)
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_template_permissions (template_code, permission_code, scope) VALUES
    ('principal', 'onb.control_tower.*', 'tenant')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
SELECT r.tenant_id, r.id, tp.permission_code, tp.scope
FROM roles r
JOIN role_template_permissions tp ON tp.template_code = 'principal' AND tp.permission_code = 'onb.control_tower.*'
WHERE r.template_code = 'principal' AND r.deleted_at IS NULL AND NOT r.is_system
ON CONFLICT DO NOTHING;

CREATE TABLE onboarding_plans (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    updated_by uuid,
    deleted_at timestamptz,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE onboarding_plans IS 'The institute''s onboarding plan (ONB-01): one live row per institute, created on first use.';
CREATE UNIQUE INDEX onboarding_plans_tenant_uq ON onboarding_plans (tenant_id) WHERE deleted_at IS NULL;
CREATE TRIGGER onboarding_plans_updated_at BEFORE UPDATE ON onboarding_plans FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE onboarding_plans ENABLE ROW LEVEL SECURITY;
CREATE POLICY onboarding_plans_tenant ON onboarding_plans USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE onboarding_domains (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    plan_id             uuid NOT NULL,
    domain              text NOT NULL CHECK (domain IN ('academic', 'students', 'staff', 'fees', 'library', 'transport', 'residential',
                                                        'cafeteria', 'inventory', 'health', 'devices', 'communication', 'certificates')),
    owner_id            uuid,
    due_date            date,
    state               text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'review', 'ready')),
    review_requested_by uuid,
    review_requested_at timestamptz,
    ready_by            uuid,
    ready_at            timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    created_by          uuid,
    updated_by          uuid,
    deleted_at          timestamptz,
    FOREIGN KEY (tenant_id, plan_id)  REFERENCES onboarding_plans (tenant_id, id),
    FOREIGN KEY (tenant_id, owner_id) REFERENCES users (tenant_id, id),
    CHECK (state <> 'ready' OR ready_at IS NOT NULL),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE onboarding_domains IS 'What people decided for one onboarding domain (ONB-01): owner, due date, review and ready marks. The status shown to users is computed from the data and this state.';
CREATE UNIQUE INDEX onboarding_domains_plan_domain_uq ON onboarding_domains (tenant_id, plan_id, domain) WHERE deleted_at IS NULL;
CREATE INDEX onboarding_domains_owner_idx ON onboarding_domains (tenant_id, owner_id) WHERE owner_id IS NOT NULL AND deleted_at IS NULL;
CREATE TRIGGER onboarding_domains_updated_at BEFORE UPDATE ON onboarding_domains FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE onboarding_domains ENABLE ROW LEVEL SECURITY;
CREATE POLICY onboarding_domains_tenant ON onboarding_domains USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
