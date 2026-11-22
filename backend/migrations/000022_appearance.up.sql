-- 000022_appearance: the institute's colour and design style, and each user's light/dark choice (decision 0005).
-- The institute picks an accent and a preset; each user picks only light, dark or system.

INSERT INTO permissions (code, module, resource, action, sensitive) VALUES
    ('iam.appearance.view',   'iam', 'appearance', 'view',   false),
    ('iam.appearance.manage', 'iam', 'appearance', 'manage', false)
ON CONFLICT (code) DO NOTHING;

ALTER TABLE users ADD COLUMN preferences jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(preferences) = 'object');
COMMENT ON COLUMN users.preferences IS 'Personal settings. themeMode: light | dark | system (absent = system).';

-- An institute session may not write the tenants row (only the platform can), so changing the look goes through this
-- narrow function: it touches only brand.primary and brand.preset of the caller's own institute, and checks both.
CREATE OR REPLACE FUNCTION set_tenant_appearance(p_accent text, p_preset text)
RETURNS jsonb
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS
$$
DECLARE
    v_brand jsonb;
BEGIN
    IF app_current_tenant() IS NULL OR app_actor_tier() <> 'tenant' THEN
        RAISE EXCEPTION 'set_tenant_appearance needs an institute session' USING ERRCODE = '42501';
    END IF;
    IF p_accent IS NULL OR p_accent !~ '^#[0-9A-Fa-f]{6}$' THEN
        RAISE EXCEPTION 'accent must be #RRGGBB' USING ERRCODE = '22023';
    END IF;
    IF p_preset IS NULL OR p_preset NOT IN ('classic', 'modern', 'contrast') THEN
        RAISE EXCEPTION 'unknown preset' USING ERRCODE = '22023';
    END IF;
    UPDATE tenants SET brand = brand || jsonb_build_object('primary', upper(p_accent), 'preset', p_preset)
    WHERE id = app_current_tenant() AND deleted_at IS NULL
    RETURNING brand INTO v_brand;
    IF v_brand IS NULL THEN
        RAISE EXCEPTION 'institute not found' USING ERRCODE = '42501';
    END IF;
    RETURN v_brand;
END
$$;
REVOKE ALL ON FUNCTION set_tenant_appearance(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION set_tenant_appearance(text, text) TO shikshit_app;
