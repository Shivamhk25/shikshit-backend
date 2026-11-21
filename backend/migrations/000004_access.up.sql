-- 000004_access: permission catalog, role templates, per-institute roles, grants, scopes, overrides.
-- Model: docs/plan/01-access-model.md. Roles are DATA. Permission codes are module.resource.action;
-- grants may use wildcards (e.g. fee.*.view, *.*.*) so the Admin role needs no special-case code.

CREATE TABLE permissions (
    code        text PRIMARY KEY CHECK (code ~ '^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$'),
    module      text NOT NULL,
    resource    text NOT NULL,
    action      text NOT NULL,
    description text,
    sensitive   boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (code = module || '.' || resource || '.' || action)
);
COMMENT ON TABLE permissions IS 'Global permission catalog. Read-only for institutes; seeded by migrations/tooling.';
CREATE INDEX permissions_module_idx ON permissions (module);
ALTER TABLE permissions ENABLE ROW LEVEL SECURITY;
CREATE POLICY permissions_read ON permissions FOR SELECT USING (true);
CREATE POLICY permissions_platform ON permissions FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

CREATE TABLE role_templates (
    code        text PRIMARY KEY CHECK (code ~ '^[a-z][a-z0-9_]*$'),
    name        text NOT NULL,
    description text,
    is_admin    boolean NOT NULL DEFAULT false,
    is_active   boolean NOT NULL DEFAULT true,
    sort_order  integer NOT NULL DEFAULT 100
);
COMMENT ON TABLE role_templates IS 'Default roles copied into every new institute (provision_tenant_roles). The admin template becomes the read-only system Admin role.';
ALTER TABLE role_templates ENABLE ROW LEVEL SECURITY;
CREATE POLICY role_templates_read ON role_templates FOR SELECT USING (true);
CREATE POLICY role_templates_platform ON role_templates FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

CREATE TABLE role_template_permissions (
    template_code   text NOT NULL REFERENCES role_templates(code) ON DELETE CASCADE,
    permission_code text NOT NULL,
    scope           text NOT NULL DEFAULT 'tenant' CHECK (scope IN ('tenant', 'campus', 'department', 'class_section', 'assigned_route', 'assigned_hostel', 'own_children', 'self', 'owned')),
    PRIMARY KEY (template_code, permission_code)
);
ALTER TABLE role_template_permissions ENABLE ROW LEVEL SECURITY;
CREATE POLICY role_template_permissions_read ON role_template_permissions FOR SELECT USING (true);
CREATE POLICY role_template_permissions_platform ON role_template_permissions FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

CREATE TABLE roles (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code          text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_]*$'),
    name          text NOT NULL,
    description   text,
    is_system     boolean NOT NULL DEFAULT false,
    template_code text REFERENCES role_templates(code) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE roles IS 'Per-institute roles. is_system roles (Admin) are read-only for institutes: clone to customise.';
CREATE UNIQUE INDEX roles_tenant_code_uq ON roles (tenant_id, code) WHERE deleted_at IS NULL;
CREATE TRIGGER roles_updated_at BEFORE UPDATE ON roles FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE roles ENABLE ROW LEVEL SECURITY;
CREATE POLICY roles_tenant ON roles USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE role_permissions (
    tenant_id       uuid NOT NULL,
    role_id         uuid NOT NULL,
    permission_code text NOT NULL,
    scope           text NOT NULL DEFAULT 'tenant' CHECK (scope IN ('tenant', 'campus', 'department', 'class_section', 'assigned_route', 'assigned_hostel', 'own_children', 'self', 'owned')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (role_id, permission_code),
    FOREIGN KEY (tenant_id, role_id) REFERENCES roles (tenant_id, id) ON DELETE CASCADE
);
COMMENT ON TABLE role_permissions IS 'Grants. permission_code is an exact code from the catalog or a wildcard pattern. One scope per (role, permission); across roles the most permissive wins.';
ALTER TABLE role_permissions ENABLE ROW LEVEL SECURITY;
CREATE POLICY role_permissions_tenant ON role_permissions USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE user_roles (
    tenant_id  uuid NOT NULL,
    user_id    uuid NOT NULL,
    role_id    uuid NOT NULL,
    starts_at  timestamptz NOT NULL DEFAULT now(),
    ends_at    timestamptz,
    granted_by uuid,
    reason     text,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, role_id) REFERENCES roles (tenant_id, id) ON DELETE CASCADE,
    CHECK (ends_at IS NULL OR ends_at > starts_at)
);
COMMENT ON TABLE user_roles IS 'A user may hold several roles (permissions and scopes union). ends_at supports acting/temporary roles.';
CREATE INDEX user_roles_role_idx ON user_roles (tenant_id, role_id);
ALTER TABLE user_roles ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_roles_tenant ON user_roles USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE user_permission_overrides (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL,
    user_id         uuid NOT NULL,
    permission_code text NOT NULL,
    effect          text NOT NULL CHECK (effect IN ('allow', 'deny')),
    scope           text NOT NULL DEFAULT 'tenant' CHECK (scope IN ('tenant', 'campus', 'department', 'class_section', 'assigned_route', 'assigned_hostel', 'own_children', 'self', 'owned')),
    expires_at      timestamptz,
    reason          text NOT NULL,
    granted_by      uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);
COMMENT ON TABLE user_permission_overrides IS 'Explicit per-user allow/deny with expiry and a mandatory reason (decision D5). Deny wins.';
CREATE INDEX user_permission_overrides_user_idx ON user_permission_overrides (tenant_id, user_id);
ALTER TABLE user_permission_overrides ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_permission_overrides_tenant ON user_permission_overrides USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE scope_assignments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    user_id    uuid NOT NULL,
    scope_type text NOT NULL CHECK (scope_type IN ('campus', 'department', 'class_section', 'assigned_route', 'assigned_hostel', 'own_children')),
    scope_ref  text NOT NULL,
    starts_at  timestamptz NOT NULL DEFAULT now(),
    ends_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (user_id, scope_type, scope_ref),
    CHECK (ends_at IS NULL OR ends_at > starts_at)
);
COMMENT ON TABLE scope_assignments IS 'Which records a user may touch: classes/sections taught, campuses, routes, hostels, linked children. scope_ref is the target id as text (target tables arrive with their modules).';
ALTER TABLE scope_assignments ENABLE ROW LEVEL SECURITY;
CREATE POLICY scope_assignments_tenant ON scope_assignments USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

-- ---------------------------------------------------------------- guards and validation
CREATE OR REPLACE FUNCTION validate_permission_code() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
BEGIN
    IF NEW.permission_code !~ '^(\*|[a-z][a-z0-9_]*)(\.(\*|[a-z][a-z0-9_]*)){0,2}$' THEN
        RAISE EXCEPTION 'invalid permission pattern: %', NEW.permission_code USING ERRCODE = 'check_violation';
    END IF;
    IF position('*' IN NEW.permission_code) = 0
       AND NOT EXISTS (SELECT 1 FROM permissions p WHERE p.code = NEW.permission_code) THEN
        RAISE EXCEPTION 'unknown permission: %', NEW.permission_code USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER role_permissions_validate BEFORE INSERT OR UPDATE ON role_permissions FOR EACH ROW EXECUTE FUNCTION validate_permission_code();
CREATE TRIGGER user_permission_overrides_validate BEFORE INSERT OR UPDATE ON user_permission_overrides FOR EACH ROW EXECUTE FUNCTION validate_permission_code();
CREATE TRIGGER role_template_permissions_validate BEFORE INSERT OR UPDATE ON role_template_permissions FOR EACH ROW EXECUTE FUNCTION validate_permission_code();

CREATE OR REPLACE FUNCTION guard_system_roles() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
BEGIN
    IF OLD.is_system AND NOT app_is_platform() THEN
        RAISE EXCEPTION 'system roles are read-only; clone the role to customise it' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END
$$;
CREATE TRIGGER roles_guard BEFORE UPDATE OR DELETE ON roles FOR EACH ROW EXECUTE FUNCTION guard_system_roles();

CREATE OR REPLACE FUNCTION guard_system_role_permissions() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
DECLARE
    v_role uuid;
    v_system boolean;
BEGIN
    v_role := CASE WHEN TG_OP = 'DELETE' THEN OLD.role_id ELSE NEW.role_id END;
    SELECT r.is_system INTO v_system FROM roles r WHERE r.id = v_role;
    IF coalesce(v_system, false) AND NOT app_is_platform() THEN
        RAISE EXCEPTION 'system roles are read-only; clone the role to customise it' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END
$$;
CREATE TRIGGER role_permissions_guard BEFORE INSERT OR UPDATE OR DELETE ON role_permissions FOR EACH ROW EXECUTE FUNCTION guard_system_role_permissions();

-- ---------------------------------------------------------------- access_version bumps
-- Clients compare X-Access-Version with the version they hold and refetch /me/access when it differs.
CREATE OR REPLACE FUNCTION bump_user_access() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
BEGIN
    UPDATE users SET access_version = access_version + 1
    WHERE tenant_id = CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END
      AND id = CASE WHEN TG_OP = 'DELETE' THEN OLD.user_id ELSE NEW.user_id END;
    RETURN NULL;
END
$$;
CREATE TRIGGER user_roles_bump AFTER INSERT OR UPDATE OR DELETE ON user_roles FOR EACH ROW EXECUTE FUNCTION bump_user_access();
CREATE TRIGGER user_permission_overrides_bump AFTER INSERT OR UPDATE OR DELETE ON user_permission_overrides FOR EACH ROW EXECUTE FUNCTION bump_user_access();
CREATE TRIGGER scope_assignments_bump AFTER INSERT OR UPDATE OR DELETE ON scope_assignments FOR EACH ROW EXECUTE FUNCTION bump_user_access();

CREATE OR REPLACE FUNCTION bump_role_holders_new() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
BEGIN
    UPDATE users u SET access_version = u.access_version + 1
    WHERE EXISTS (SELECT 1 FROM user_roles ur
                  WHERE ur.tenant_id = u.tenant_id AND ur.user_id = u.id
                    AND ur.role_id IN (SELECT DISTINCT role_id FROM new_rows));
    RETURN NULL;
END
$$;
CREATE OR REPLACE FUNCTION bump_role_holders_old() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
BEGIN
    UPDATE users u SET access_version = u.access_version + 1
    WHERE EXISTS (SELECT 1 FROM user_roles ur
                  WHERE ur.tenant_id = u.tenant_id AND ur.user_id = u.id
                    AND ur.role_id IN (SELECT DISTINCT role_id FROM old_rows));
    RETURN NULL;
END
$$;
CREATE TRIGGER role_permissions_bump_ins AFTER INSERT ON role_permissions REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION bump_role_holders_new();
CREATE TRIGGER role_permissions_bump_upd AFTER UPDATE ON role_permissions REFERENCING NEW TABLE AS new_rows OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION bump_role_holders_new();
CREATE TRIGGER role_permissions_bump_del AFTER DELETE ON role_permissions REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION bump_role_holders_old();

CREATE OR REPLACE FUNCTION bump_tenant_access() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
BEGIN
    UPDATE users SET access_version = access_version + 1
    WHERE tenant_id = CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    RETURN NULL;
END
$$;
CREATE TRIGGER tenant_modules_bump AFTER INSERT OR UPDATE OR DELETE ON tenant_modules FOR EACH ROW EXECUTE FUNCTION bump_tenant_access();

-- ---------------------------------------------------------------- provisioning
-- Copies the role templates into a new institute. Idempotent: roles that already exist are left untouched.
CREATE OR REPLACE FUNCTION provision_tenant_roles(p_tenant uuid) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
DECLARE
    v_created integer;
BEGIN
    IF NOT app_is_platform() THEN
        RAISE EXCEPTION 'only the platform tier may provision roles' USING ERRCODE = 'insufficient_privilege';
    END IF;

    WITH ins AS (
        INSERT INTO roles (tenant_id, code, name, description, is_system, template_code)
        SELECT p_tenant, t.code, t.name, t.description, t.is_admin, t.code
        FROM role_templates t
        WHERE t.is_active
        ON CONFLICT (tenant_id, code) WHERE deleted_at IS NULL DO NOTHING
        RETURNING id, tenant_id, template_code
    ), perms AS (
        INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
        SELECT ins.tenant_id, ins.id, tp.permission_code, tp.scope
        FROM ins JOIN role_template_permissions tp ON tp.template_code = ins.template_code
        RETURNING 1
    )
    SELECT count(*) INTO v_created FROM ins;
    RETURN v_created;
END
$$;
REVOKE ALL ON FUNCTION provision_tenant_roles(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION provision_tenant_roles(uuid) TO shikshit_app;
