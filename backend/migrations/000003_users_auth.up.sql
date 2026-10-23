-- 000003_users_auth.up.sql
-- Users, roles, authentication, and sessions

-- Roles enum
CREATE TYPE user_role AS ENUM (
    'super_admin',
    'institute_admin',
    'principal',
    'teacher',
    'student',
    'parent',
    'accountant',
    'registrar',
    'hr'
);

COMMENT ON TYPE user_role IS 'System roles with hierarchical permissions';

-- Users table
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    role user_role NOT NULL DEFAULT 'student',
    
    -- Identity
    email CITEXT UNIQUE NOT NULL,
    username VARCHAR(50) UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    first_name VARCHAR(100) NOT NULL,
    last_name VARCHAR(100) NOT NULL,
    middle_name VARCHAR(100),
    display_name VARCHAR(200),
    avatar_url VARCHAR(500),
    
    -- Contact
    phone VARCHAR(30),
    phone_verified BOOLEAN DEFAULT false,
    email_verified BOOLEAN DEFAULT false,
    
    -- Status
    is_active BOOLEAN DEFAULT true,
    is_locked BOOLEAN DEFAULT false,
    locked_until TIMESTAMPTZ,
    failed_login_attempts INTEGER DEFAULT 0,
    last_login_at TIMESTAMPTZ,
    last_login_ip INET,
    
    -- Security
    password_changed_at TIMESTAMPTZ DEFAULT now(),
    must_change_password BOOLEAN DEFAULT false,
    mfa_enabled BOOLEAN DEFAULT false,
    mfa_secret VARCHAR(255),
    backup_codes VARCHAR(255)[],
    
    -- Metadata
    metadata JSONB DEFAULT '{}'::jsonb,
    
    -- Timestamps
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

COMMENT ON TABLE users IS 'System users with role-based access';
COMMENT ON COLUMN users.institute_id IS 'Tenant reference (NULL for super_admin)';
COMMENT ON COLUMN users.role IS 'Primary role (additional roles via user_roles)';
COMMENT ON COLUMN users.metadata IS 'User preferences, accessibility settings, UI state';

-- Indexes
CREATE INDEX idx_users_institute_role ON users(institute_id, role) WHERE is_deleted IS NULL;
CREATE INDEX idx_users_email ON users(email) WHERE is_deleted IS NULL;
CREATE INDEX idx_users_username ON users(username) WHERE is_deleted IS NULL;
CREATE INDEX idx_users_active ON users(institute_id, is_active) WHERE is_deleted IS NULL;
CREATE INDEX idx_users_metadata_gin ON users USING GIN (metadata);

-- User additional roles (many-to-many)
CREATE TABLE user_roles (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role user_role NOT NULL,
    institute_id UUID REFERENCES institutes(id) ON DELETE CASCADE,
    granted_by UUID REFERENCES users(id),
    granted_at TIMESTAMPTZ DEFAULT now(),
    expires_at TIMESTAMPTZ,
    PRIMARY KEY (user_id, role, institute_id)
);

COMMENT ON TABLE user_roles IS 'Additional roles for users (multi-role support)';
CREATE INDEX idx_user_roles_institute ON user_roles(institute_id, role);

-- Sessions (refresh tokens)
CREATE TABLE sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    
    refresh_token_hash VARCHAR(255) UNIQUE NOT NULL,
    user_agent TEXT,
    ip_address INET,
    
    is_revoked BOOLEAN DEFAULT false,
    revoked_at TIMESTAMPTZ,
    revoked_reason VARCHAR(100),
    
    expires_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    
    device_info JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE sessions IS 'Refresh token sessions for authentication';
CREATE INDEX idx_sessions_user ON sessions(user_id) WHERE is_revoked = false;
CREATE INDEX idx_sessions_token ON sessions(refresh_token_hash) WHERE is_revoked = false;
CREATE INDEX idx_sessions_expires ON sessions(expires_at) WHERE is_revoked = false;

-- Password reset tokens
CREATE TABLE password_reset_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash VARCHAR(255) UNIQUE NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE password_reset_tokens IS 'One-time password reset tokens';
CREATE INDEX idx_password_reset_user ON password_reset_tokens(user_id) WHERE used_at IS NULL;

-- Email verification tokens
CREATE TABLE email_verification_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash VARCHAR(255) UNIQUE NOT NULL,
    email CITEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE email_verification_tokens IS 'Email verification tokens';
CREATE INDEX idx_email_verification_user ON email_verification_tokens(user_id) WHERE verified_at IS NULL;

-- Audit log for security events
CREATE TABLE auth_audit_log (
    id BIGSERIAL PRIMARY KEY,
    institute_id UUID REFERENCES institutes(id) ON DELETE SET NULL,
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    
    event_type VARCHAR(50) NOT NULL,
    event_status VARCHAR(20) NOT NULL,
    
    ip_address INET,
    user_agent TEXT,
    location VARCHAR(200),
    
    details JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE auth_audit_log IS 'Security audit trail for authentication events';
CREATE INDEX idx_auth_audit_user ON auth_audit_log(user_id, created_at DESC);
CREATE INDEX idx_auth_audit_institute ON auth_audit_log(institute_id, created_at DESC);
CREATE INDEX idx_auth_audit_type ON auth_audit_log(event_type, created_at DESC);

-- Enable RLS
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE password_reset_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE email_verification_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE auth_audit_log ENABLE ROW LEVEL SECURITY;

-- Users: super_admin sees all, others see own institute
CREATE POLICY users_super_admin ON users
    USING (current_setting('app.current_role', true) = 'super_admin');

CREATE POLICY users_tenant_isolation ON users
    USING (
        institute_id = current_setting('app.current_institute_id', true)::UUID
        OR current_setting('app.current_role', true) = 'super_admin'
    );

CREATE POLICY users_self_access ON users
    USING (id = current_setting('app.current_user_id', true)::UUID)
    WITH CHECK (id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY user_roles_tenant ON user_roles
    USING (
        institute_id = current_setting('app.current_institute_id', true)::UUID
        OR institute_id IS NULL
        OR current_setting('app.current_role', true) = 'super_admin'
    );

CREATE POLICY sessions_self ON sessions
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY sessions_admin ON sessions
    USING (
        institute_id = current_setting('app.current_institute_id', true)::UUID
        AND current_setting('app.current_role', true) IN ('institute_admin', 'principal', 'hr')
    );

CREATE POLICY auth_audit_admin ON auth_audit_log
    USING (
        institute_id = current_setting('app.current_institute_id', true)::UUID
        AND current_setting('app.current_role', true) IN ('institute_admin', 'principal', 'hr', 'super_admin')
    );

CREATE POLICY auth_audit_self ON auth_audit_log
    USING (user_id = current_setting('app.current_user_id', true)::UUID);