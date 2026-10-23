-- 000002_institutes.up.sql
-- Core institutes (tenants) table

CREATE TABLE institutes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code VARCHAR(50) UNIQUE NOT NULL,              -- 'springs-high', 'oak-univ'
    name VARCHAR(200) NOT NULL,
    domain VARCHAR(100) UNIQUE,                    -- 'springs.example.com'
    logo_url VARCHAR(500),
    timezone VARCHAR(50) DEFAULT 'UTC',
    locale VARCHAR(10) DEFAULT 'en',
    settings JSONB DEFAULT '{}'::jsonb,            -- features, branding, configs
    subscription_tier VARCHAR(20) DEFAULT 'standard', -- standard, premium, enterprise
    subscription_status VARCHAR(20) DEFAULT 'active', -- active, past_due, canceled, trialing
    subscription_ends_at TIMESTAMPTZ,
    max_users INTEGER DEFAULT 1000,
    max_storage_mb INTEGER DEFAULT 10240,
    is_active BOOLEAN DEFAULT true,
    is_deleted BOOLEAN DEFAULT false,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

COMMENT ON TABLE institutes IS 'Educational institutions (tenants)';
COMMENT ON COLUMN institutes.code IS 'Unique short code for subdomain/identification';
COMMENT ON COLUMN institutes.settings IS 'JSON configuration: features, branding, integrations';
COMMENT ON COLUMN institutes.subscription_tier IS 'Pricing tier: standard, premium, enterprise';
COMMENT ON COLUMN institutes.max_users IS 'Maximum allowed users for this institute';

-- Indexes
CREATE INDEX idx_institutes_code ON institutes(code) WHERE is_deleted = false;
CREATE INDEX idx_institutes_domain ON institutes(domain) WHERE is_deleted = false;
CREATE INDEX idx_institutes_active ON institutes(is_active) WHERE is_deleted = false;

-- Updated at trigger
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER update_institutes_updated_at
    BEFORE UPDATE ON institutes
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- Enable RLS
ALTER TABLE institutes ENABLE ROW LEVEL SECURITY;

-- Super admins can see all institutes
CREATE POLICY institute_super_admin ON institutes
    USING (current_setting('app.current_role', true) = 'super_admin');

-- Users can only see their own institute
CREATE POLICY institute_tenant_isolation ON institutes
    USING (id = current_setting('app.current_institute_id', true)::UUID);