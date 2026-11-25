-- Go-live gate and sign-off (ONB-23). A sign-off records who confirmed the institute is ready to go live, when, their
-- notes and a snapshot of the readiness at that moment. It can be revoked; a new sign-off is then a re-run. At most one
-- sign-off is active per institute. The institute's own status (tenants.status) moves onboarding -> live only through
-- onb_set_tenant_status, because tenants are writable by platform sessions only.
--
-- Permissions: onb.golive.view (see the gate) and onb.golive.signoff (sign off and revoke, sensitive). The Admin holds
-- them through its *.*.* grant; the Principal may also sign off (founder A-29 answer for the control tower, same people).

INSERT INTO permissions (code, module, resource, action, sensitive) VALUES
    ('onb.golive.view',    'onb', 'golive', 'view',    false),
    ('onb.golive.signoff', 'onb', 'golive', 'signoff', true)
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_template_permissions (template_code, permission_code, scope) VALUES
    ('principal', 'onb.golive.*', 'tenant')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
SELECT r.tenant_id, r.id, tp.permission_code, tp.scope
FROM roles r
JOIN role_template_permissions tp ON tp.template_code = 'principal' AND tp.permission_code = 'onb.golive.*'
WHERE r.template_code = 'principal' AND r.deleted_at IS NULL AND NOT r.is_system
ON CONFLICT DO NOTHING;

CREATE TABLE golive_signoffs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    signed_by     uuid NOT NULL,
    signed_at     timestamptz NOT NULL DEFAULT now(),
    notes         text NOT NULL DEFAULT '' CHECK (length(notes) <= 2000),
    snapshot      jsonb NOT NULL,
    revoked_by    uuid,
    revoked_at    timestamptz,
    revoke_reason text CHECK (revoke_reason IS NULL OR length(revoke_reason) <= 2000),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    FOREIGN KEY (tenant_id, signed_by)  REFERENCES users (tenant_id, id),
    FOREIGN KEY (tenant_id, revoked_by) REFERENCES users (tenant_id, id),
    CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE golive_signoffs IS 'Go-live sign-offs (ONB-23): who, when, notes and the readiness snapshot at that moment. Revoked rows stay as history.';
CREATE UNIQUE INDEX golive_signoffs_active_uq ON golive_signoffs (tenant_id) WHERE revoked_at IS NULL AND deleted_at IS NULL;
CREATE INDEX golive_signoffs_tenant_idx ON golive_signoffs (tenant_id, signed_at DESC);
CREATE TRIGGER golive_signoffs_updated_at BEFORE UPDATE ON golive_signoffs FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE golive_signoffs ENABLE ROW LEVEL SECURITY;
CREATE POLICY golive_signoffs_tenant ON golive_signoffs USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

-- The only way a tenant session changes its own status, and only between onboarding and live. Returns whether a row moved.
CREATE OR REPLACE FUNCTION onb_set_tenant_status(p_from text, p_to text)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
DECLARE n integer;
BEGIN
    IF (p_from, p_to) NOT IN (('onboarding', 'live'), ('live', 'onboarding')) THEN
        RAISE EXCEPTION 'status change not allowed' USING ERRCODE = '22023';
    END IF;
    UPDATE tenants
       SET status = p_to,
           went_live_at = CASE WHEN p_to = 'live' THEN COALESCE(went_live_at, now()) ELSE went_live_at END
     WHERE id = app_current_tenant() AND status = p_from AND deleted_at IS NULL;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n = 1;
END
$$;
REVOKE ALL ON FUNCTION onb_set_tenant_status(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION onb_set_tenant_status(text, text) TO shikshit_app;
