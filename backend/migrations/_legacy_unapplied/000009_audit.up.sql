-- 000009_audit.up.sql
-- Comprehensive audit logging for compliance (FERPA, GDPR)

-- Main audit log (append-only)
CREATE TABLE audit_log (
    id BIGSERIAL PRIMARY KEY,
    institute_id UUID REFERENCES institutes(id) ON DELETE SET NULL,
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    session_id UUID,
    
    -- Action
    action VARCHAR(100) NOT NULL,       -- CREATE, READ, UPDATE, DELETE, LOGIN, LOGOUT, EXPORT, PRINT, etc.
    action_category VARCHAR(50),        -- data_access, data_modification, auth, admin, config, export
    resource_type VARCHAR(100) NOT NULL, -- users, students, grades, fees, attendance, etc.
    resource_id UUID,                   -- ID of affected resource
    resource_identifier VARCHAR(200),   -- Human-readable identifier (email, name, etc.)
    
    -- Changes
    old_values JSONB,                   -- Previous state
    new_values JSONB,                   -- New state
    changed_fields TEXT[],              -- List of changed field names
    
    -- Context
    ip_address INET,
    user_agent TEXT,
    request_id UUID,
    endpoint VARCHAR(200),
    method VARCHAR(10),
    
    -- Status
    status VARCHAR(20) DEFAULT 'success', -- success, failure, denied
    error_message TEXT,
    
    -- Compliance
    data_classification VARCHAR(30) DEFAULT 'internal', -- public, internal, confidential, restricted
    contains_pii BOOLEAN DEFAULT false,
    pii_fields TEXT[],                  -- Which fields contain PII
    legal_basis VARCHAR(50),            -- consent, legitimate_interest, legal_obligation, vital_interest
    
    -- Timing
    timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
    duration_ms INTEGER,                -- Operation duration
    
    -- Partitioning (monthly)
    UNIQUE (institute_id, id)
);

COMMENT ON TABLE audit_log IS 'Comprehensive audit trail for compliance';
COMMENT ON COLUMN audit_log.legal_basis IS 'GDPR Article 6 legal basis for processing';

-- Indexes
CREATE INDEX idx_audit_institute_time ON audit_log(institute_id, timestamp DESC);
CREATE INDEX idx_audit_user_time ON audit_log(user_id, timestamp DESC);
CREATE INDEX idx_audit_resource ON audit_log(resource_type, resource_id, timestamp DESC);
CREATE INDEX idx_audit_action ON audit_log(action, timestamp DESC);
CREATE INDEX idx_audit_status ON audit_log(status) WHERE status != 'success';
CREATE INDEX idx_audit_pii ON audit_log(contains_pii, timestamp DESC) WHERE contains_pii = true;

-- Convert to hypertable
-- SELECT create_hypertable('audit_log', 'timestamp',
--     chunk_time_interval => INTERVAL '1 day',
--     if_not_exists => TRUE);

-- Data access log (for FERPA - who accessed student records)
CREATE TABLE data_access_log (
    id BIGSERIAL PRIMARY KEY,
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    accessor_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    student_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    access_type VARCHAR(50) NOT NULL,   -- view, export, print, download, api
    resource_type VARCHAR(50) NOT NULL, -- grades, attendance, pii, discipline, health, all
    fields_accessed TEXT[],             -- Specific fields viewed
    
    purpose VARCHAR(100),               -- educational, administrative, health_safety, legal
    legal_basis VARCHAR(50),            -- FERPA exception, consent, directory_info
    
    ip_address INET,
    user_agent TEXT,
    session_id UUID,
    
    timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
    
    UNIQUE (institute_id, id)
);

COMMENT ON TABLE data_access_log IS 'FERPA-compliant student data access tracking';
COMMENT ON COLUMN data_access_log.purpose IS 'Educational purpose for access (FERPA)';

CREATE INDEX idx_data_access_student ON data_access_log(student_id, timestamp DESC);
CREATE INDEX idx_data_access_accessor ON data_access_log(accessor_id, timestamp DESC);
CREATE INDEX idx_data_access_type ON data_access_log(access_type, timestamp DESC);

-- Convert to hypertable
-- SELECT create_hypertable('data_access_log', 'timestamp',
--     chunk_time_interval => INTERVAL '1 day',
--     if_not_exists => TRUE);

-- Consent management (GDPR)
CREATE TABLE consents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    consent_type VARCHAR(50) NOT NULL,  -- marketing, analytics, third_party_sharing, photos, directory
    consent_version VARCHAR(20) NOT NULL,
    consent_text_hash VARCHAR(64) NOT NULL, -- SHA-256 of consent text
    
    status VARCHAR(20) NOT NULL,        -- granted, denied, withdrawn, expired
    granted_at TIMESTAMPTZ,
    denied_at TIMESTAMPTZ,
    withdrawn_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    
    -- How consent was given
    method VARCHAR(30) DEFAULT 'explicit', -- explicit, implicit, opt_out, legitimate_interest
    ip_address INET,
    user_agent TEXT,
    witness_id UUID REFERENCES users(id) ON DELETE SET NULL, -- For minors
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (institute_id, user_id, consent_type, consent_version)
);

COMMENT ON TABLE consents IS 'GDPR consent management';

CREATE INDEX idx_consents_user ON consents(user_id, consent_type, status);
CREATE INDEX idx_consents_status ON consents(status) WHERE status = 'granted';

-- Data subject requests (GDPR)
CREATE TABLE data_subject_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    request_type VARCHAR(30) NOT NULL,  -- access, rectification, erasure, portability, restriction, objection
    status VARCHAR(20) DEFAULT 'pending', -- pending, in_progress, completed, rejected, withdrawn
    
    description TEXT,
    requested_data_categories TEXT[],
    
    -- Verification
    verified_at TIMESTAMPTZ,
    verified_by UUID REFERENCES users(id) ON DELETE SET NULL,
    verification_method VARCHAR(50),    -- email, phone, in_person, document
    
    -- Processing
    assigned_to UUID REFERENCES users(id) ON DELETE SET NULL,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    rejected_at TIMESTAMPTZ,
    rejection_reason TEXT,
    
    -- Output
    output_format VARCHAR(20),          -- json, csv, pdf
    output_url VARCHAR(500),
    output_expires_at TIMESTAMPTZ,
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE data_subject_requests IS 'GDPR Data Subject Access Requests (DSAR)';

CREATE INDEX idx_dsr_user ON data_subject_requests(user_id, status);
CREATE INDEX idx_dsr_status ON data_subject_requests(status) WHERE status IN ('pending', 'in_progress');

-- Data retention policies
CREATE TABLE retention_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    
    resource_type VARCHAR(100) NOT NULL,
    description TEXT,
    
    retention_period INTERVAL NOT NULL, -- e.g., '7 years', '3 years'
    retention_start VARCHAR(50) DEFAULT 'created_at', -- created_at, updated_at, closed_at, last_activity
    
    -- Disposal
    disposal_action VARCHAR(30) DEFAULT 'delete', -- delete, anonymize, archive
    archive_location VARCHAR(200),
    
    -- Legal
    legal_basis VARCHAR(100),
    regulation VARCHAR(50),             -- FERPA, GDPR, state_law, internal_policy
    
    -- Exceptions
    exceptions JSONB DEFAULT '[]'::jsonb, -- Legal hold, litigation, etc.
    
    is_active BOOLEAN DEFAULT true,
    is_system BOOLEAN DEFAULT false,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (institute_id, resource_type)
);

COMMENT ON TABLE retention_policies IS 'Data retention and disposal policies';

-- Retention execution log
CREATE TABLE retention_executions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_id UUID NOT NULL REFERENCES retention_policies(id) ON DELETE CASCADE,
    
    status VARCHAR(20) DEFAULT 'pending', -- pending, running, completed, failed, partial
    started_at TIMESTAMPTZ DEFAULT now(),
    completed_at TIMESTAMPTZ,
    
    records_affected BIGINT DEFAULT 0,
    records_deleted BIGINT DEFAULT 0,
    records_anonymized BIGINT DEFAULT 0,
    records_archived BIGINT DEFAULT 0,
    
    error_message TEXT,
    details JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE retention_executions IS 'Retention policy execution history';

-- Enable RLS
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE data_access_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE consents ENABLE ROW LEVEL SECURITY;
ALTER TABLE data_subject_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_executions ENABLE ROW LEVEL SECURITY;

-- RLS Policies (only admins and compliance officers can view)
CREATE POLICY audit_admin ON audit_log
    USING (
        current_setting('app.current_role', true) IN ('super_admin', 'institute_admin', 'principal', 'registrar')
        AND institute_id = current_setting('app.current_institute_id', true)::UUID
    );

CREATE POLICY data_access_admin ON data_access_log
    USING (
        current_setting('app.current_role', true) IN ('super_admin', 'institute_admin', 'principal', 'registrar')
        AND institute_id = current_setting('app.current_institute_id', true)::UUID
    );

CREATE POLICY consents_user ON consents
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY consents_admin ON consents
    USING (
        current_setting('app.current_role', true) IN ('super_admin', 'institute_admin', 'principal', 'registrar')
        AND institute_id = current_setting('app.current_institute_id', true)::UUID
    );

CREATE POLICY dsr_user ON data_subject_requests
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY dsr_admin ON data_subject_requests
    USING (
        current_setting('app.current_role', true) IN ('super_admin', 'institute_admin', 'principal', 'registrar')
        AND institute_id = current_setting('app.current_institute_id', true)::UUID
    );

CREATE POLICY retention_admin ON retention_policies
    USING (
        current_setting('app.current_role', true) IN ('super_admin', 'institute_admin')
        AND institute_id = current_setting('app.current_institute_id', true)::UUID
    );

CREATE POLICY retention_exec_admin ON retention_executions
    USING (
        current_setting('app.current_role', true) IN ('super_admin', 'institute_admin')
        AND policy_id IN (SELECT id FROM retention_policies WHERE institute_id = current_setting('app.current_institute_id', true)::UUID)
    );