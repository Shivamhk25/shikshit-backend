-- 000024_support_visits: SAD-05 "view as school" (decision 0018, APPROVALS A-11 option A). A platform user gives a
-- reason and gets a one-time code; redeemed on the school's own address it opens a 30-minute, read-only session.
-- Every read is in the school's audit trail with the reason. Rows are written only through the functions below.

CREATE TABLE support_visits (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    platform_user_id   uuid NOT NULL REFERENCES platform_users(id),
    platform_user_name text NOT NULL,
    reason             text NOT NULL CHECK (length(btrim(reason)) BETWEEN 10 AND 500),
    code_hash          text NOT NULL UNIQUE,
    code_used_at       timestamptz,
    started_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    ended_at           timestamptz
);
COMMENT ON TABLE support_visits IS 'Read-only support visits by platform users (SAD-05). code_hash is SHA-256 of the one-time code. The school sees its visits; only the platform creates them.';
CREATE INDEX support_visits_tenant_idx ON support_visits (tenant_id, started_at DESC);
ALTER TABLE support_visits ENABLE ROW LEVEL SECURITY;
CREATE POLICY support_visits_platform ON support_visits FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());
CREATE POLICY support_visits_tenant_read ON support_visits FOR SELECT USING (tenant_id = app_current_tenant());

-- Redeeming happens on the school's address (an institute session): the code works once, only for this school and
-- only before the visit expires.
CREATE OR REPLACE FUNCTION redeem_support_visit(p_code_hash text)
RETURNS TABLE (id uuid, platform_user_id uuid, platform_user_name text, reason text, expires_at timestamptz)
LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
    UPDATE support_visits v SET code_used_at = now()
    WHERE v.code_hash = p_code_hash AND v.tenant_id = app_current_tenant() AND app_actor_tier() = 'tenant'
      AND v.code_used_at IS NULL AND v.ended_at IS NULL AND v.expires_at > now()
    RETURNING v.id, v.platform_user_id, v.platform_user_name, v.reason, v.expires_at
$$;
REVOKE ALL ON FUNCTION redeem_support_visit(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION redeem_support_visit(text) TO shikshit_app;

-- Ending a visit from inside it (the "End visit" button).
CREATE OR REPLACE FUNCTION end_support_visit(p_id uuid)
RETURNS boolean
LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
    WITH e AS (
        UPDATE support_visits v SET ended_at = now()
        WHERE v.id = p_id AND v.tenant_id = app_current_tenant() AND app_actor_tier() = 'tenant' AND v.ended_at IS NULL
        RETURNING 1
    ) SELECT EXISTS (SELECT 1 FROM e)
$$;
REVOKE ALL ON FUNCTION end_support_visit(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION end_support_visit(uuid) TO shikshit_app;
