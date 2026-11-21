-- Lets an institute change its own import-approval switch (tenants.settings.importApprovalRequired) and nothing else.
-- Institutes cannot write to tenants (platform only, by design), so this narrow function does the one allowed write.
-- It touches only the caller's own institute (the one bound to the session) and only this key. WHO may call it is
-- decided by the API (onb.maker_checker.manage); the audit row is written by the caller in the same transaction.
CREATE OR REPLACE FUNCTION set_import_approval_required(p_required boolean) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
DECLARE
    v_tenant uuid := app_current_tenant();
BEGIN
    IF v_tenant IS NULL THEN
        RAISE EXCEPTION 'no institute is bound to this session' USING ERRCODE = 'insufficient_privilege';
    END IF;
    UPDATE tenants
    SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{importApprovalRequired}', to_jsonb(p_required))
    WHERE id = v_tenant AND deleted_at IS NULL;
END
$$;
REVOKE ALL ON FUNCTION set_import_approval_required(boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION set_import_approval_required(boolean) TO shikshit_app;
