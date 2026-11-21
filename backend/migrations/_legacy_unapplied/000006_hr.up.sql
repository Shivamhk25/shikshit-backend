-- 000006_hr.up.sql
-- HR module: staff, leave, contracts, performance, recruitment

-- Staff profiles (extends users)
CREATE TABLE staff_profiles (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    employee_id VARCHAR(50) UNIQUE NOT NULL,
    department_id UUID REFERENCES departments(id) ON DELETE SET NULL,
    designation VARCHAR(100),
    grade_level VARCHAR(20),
    employment_type VARCHAR(30) DEFAULT 'full_time', -- full_time, part_time, contract, temporary, probation
    join_date DATE NOT NULL,
    confirmation_date DATE,
    termination_date DATE,
    termination_reason TEXT,
    reporting_to UUID REFERENCES users(id) ON DELETE SET NULL,
    
    -- Compensation
    basic_salary DECIMAL(12,2),
    pay_scale VARCHAR(50),
    pay_frequency VARCHAR(20) DEFAULT 'monthly',
    bank_account VARCHAR(50),
    bank_ifsc VARCHAR(20),
    pan_number VARCHAR(20),
    pf_number VARCHAR(30),
    esi_number VARCHAR(30),
    
    -- Documents
    documents JSONB DEFAULT '{}'::jsonb, -- contracts, certificates, id proofs
    
    -- Emergency contact
    emergency_contact_name VARCHAR(200),
    emergency_contact_phone VARCHAR(30),
    emergency_contact_relation VARCHAR(50),
    
    -- Status
    status VARCHAR(20) DEFAULT 'active', -- active, on_leave, suspended, terminated, resigned
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE staff_profiles IS 'Extended staff information';
CREATE INDEX idx_staff_institute ON staff_profiles(institute_id, status);
CREATE INDEX idx_staff_department ON staff_profiles(department_id);
CREATE INDEX idx_staff_reporting ON staff_profiles(reporting_to);

-- Leave types
CREATE TABLE leave_types (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    code VARCHAR(20) NOT NULL,
    description TEXT,
    category VARCHAR(30) DEFAULT 'paid', -- paid, unpaid, medical, maternity, paternity, bereavement, study, compensatory
    max_days_per_year DECIMAL(6,2),
    max_days_per_month DECIMAL(6,2),
    carry_forward BOOLEAN DEFAULT false,
    max_carry_forward_days DECIMAL(6,2),
    requires_approval BOOLEAN DEFAULT true,
    approval_levels INTEGER DEFAULT 1,    -- 1 = manager, 2 = manager + HR, etc.
    is_active BOOLEAN DEFAULT true,
    settings JSONB DEFAULT '{}'::jsonb,  -- half-day rules, minimum notice, etc.
    UNIQUE (institute_id, code)
);

COMMENT ON TABLE leave_types IS 'Types of leave available';
COMMENT ON COLUMN leave_types.approval_levels IS 'Number of approval levels required';

-- Leave balances
CREATE TABLE leave_balances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    leave_type_id UUID NOT NULL REFERENCES leave_types(id) ON DELETE CASCADE,
    academic_year_id UUID NOT NULL REFERENCES academic_years(id) ON DELETE CASCADE,
    
    total_entitled DECIMAL(6,2) DEFAULT 0,
    carried_forward DECIMAL(6,2) DEFAULT 0,
    earned DECIMAL(6,2) DEFAULT 0,
    used DECIMAL(6,2) DEFAULT 0,
    balance DECIMAL(6,2) GENERATED ALWAYS AS (total_entitled + carried_forward + earned - used) STORED,
    
    last_updated TIMESTAMPTZ DEFAULT now(),
    UNIQUE (user_id, leave_type_id, academic_year_id)
);

COMMENT ON TABLE leave_balances IS 'Leave balance per user per year';
CREATE INDEX idx_leave_balances_user ON leave_balances(user_id, academic_year_id);

-- Leave requests
CREATE TABLE leave_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    leave_type_id UUID NOT NULL REFERENCES leave_types(id) ON DELETE RESTRICT,
    academic_year_id UUID NOT NULL REFERENCES academic_years(id) ON DELETE CASCADE,
    
    start_date DATE NOT NULL,
    end_date DATE NOT NULL,
    start_period VARCHAR(10),           -- 'full', 'morning', 'afternoon'
    end_period VARCHAR(10),
    total_days DECIMAL(4,2) NOT NULL,
    
    reason TEXT NOT NULL,
    supporting_document_urls TEXT[],
    
    status VARCHAR(20) DEFAULT 'pending', -- pending, approved, rejected, cancelled, withdrawn
    applied_at TIMESTAMPTZ DEFAULT now(),
    
    -- Approval chain
    approved_by UUID REFERENCES users(id) ON DELETE SET NULL,
    approved_at TIMESTAMPTZ,
    rejected_by UUID REFERENCES users(id) ON DELETE SET NULL,
    rejected_at TIMESTAMPTZ,
    rejection_reason TEXT,
    
    -- Cancellation
    cancelled_by UUID REFERENCES users(id) ON DELETE SET NULL,
    cancelled_at TIMESTAMPTZ,
    cancellation_reason TEXT,
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE leave_requests IS 'Leave applications and approvals';
CREATE INDEX idx_leave_requests_user ON leave_requests(user_id, status, start_date);
CREATE INDEX idx_leave_requests_dates ON leave_requests(start_date, end_date);
CREATE INDEX idx_leave_requests_pending ON leave_requests(status) WHERE status = 'pending';

-- Leave approval workflow
CREATE TABLE leave_approvals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    leave_request_id UUID NOT NULL REFERENCES leave_requests(id) ON DELETE CASCADE,
    level INTEGER NOT NULL,             -- 1, 2, 3...
    approver_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    action VARCHAR(20) NOT NULL,        -- approved, rejected, delegated
    comments TEXT,
    actioned_at TIMESTAMPTZ DEFAULT now(),
    delegated_to UUID REFERENCES users(id) ON DELETE SET NULL,
    UNIQUE (leave_request_id, level)
);

COMMENT ON TABLE leave_approvals IS 'Multi-level leave approval trail';

-- Contracts
CREATE TABLE contracts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    contract_number VARCHAR(50) UNIQUE NOT NULL,
    
    type VARCHAR(30) NOT NULL,          -- permanent, contract, probation, internship, consultant
    start_date DATE NOT NULL,
    end_date DATE,
    notice_period_days INTEGER DEFAULT 30,
    
    -- Terms
    basic_salary DECIMAL(12,2),
    allowances JSONB DEFAULT '{}'::jsonb,
    benefits JSONB DEFAULT '{}'::jsonb,
    working_hours_per_week DECIMAL(4,1) DEFAULT 40,
    working_days_per_week INTEGER DEFAULT 5,
    
    -- Probation
    has_probation BOOLEAN DEFAULT false,
    probation_period_days INTEGER DEFAULT 90,
    probation_end_date DATE,
    probation_status VARCHAR(20),       -- pending, confirmed, extended, terminated
    
    -- Status
    status VARCHAR(20) DEFAULT 'draft', -- draft, active, expired, terminated, renewed
    signed_by_employer UUID REFERENCES users(id) ON DELETE SET NULL,
    signed_by_employer_at TIMESTAMPTZ,
    signed_by_employee UUID REFERENCES users(id) ON DELETE SET NULL,
    signed_by_employee_at TIMESTAMPTZ,
    
    document_url VARCHAR(500),
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE contracts IS 'Employment contracts';
CREATE INDEX idx_contracts_user ON contracts(user_id, status);
CREATE INDEX idx_contracts_dates ON contracts(start_date, end_date);

-- Performance reviews
CREATE TABLE performance_reviews (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    academic_year_id UUID NOT NULL REFERENCES academic_years(id) ON DELETE CASCADE,
    employee_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reviewer_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    review_type VARCHAR(30) DEFAULT 'annual', -- annual, mid_year, probation, project
    period_start DATE NOT NULL,
    period_end DATE NOT NULL,
    
    status VARCHAR(20) DEFAULT 'draft', -- draft, in_progress, submitted, acknowledged, finalized
    
    -- Scores (1-5 or 1-10 scale)
    overall_rating DECIMAL(3,2),
    goals_rating DECIMAL(3,2),
    competencies_rating DECIMAL(3,2),
    behavior_rating DECIMAL(3,2),
    
    -- Content
    achievements TEXT,
    strengths TEXT,
    areas_for_improvement TEXT,
    goals_next_period TEXT,
    reviewer_comments TEXT,
    employee_comments TEXT,
    
    -- Sign-off
    submitted_at TIMESTAMPTZ,
    acknowledged_at TIMESTAMPTZ,
    finalized_at TIMESTAMPTZ,
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE performance_reviews IS 'Performance review cycles';
CREATE INDEX idx_performance_employee ON performance_reviews(employee_id, academic_year_id);
CREATE INDEX idx_performance_reviewer ON performance_reviews(reviewer_id, status);

-- Recruitment
CREATE TABLE job_postings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    department_id UUID REFERENCES departments(id) ON DELETE SET NULL,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    
    title VARCHAR(200) NOT NULL,
    code VARCHAR(30) UNIQUE NOT NULL,
    description TEXT,
    requirements TEXT,
    responsibilities TEXT,
    
    employment_type VARCHAR(30) DEFAULT 'full_time',
    experience_min_years INTEGER DEFAULT 0,
    experience_max_years INTEGER,
    salary_min DECIMAL(12,2),
    salary_max DECIMAL(12,2),
    salary_currency VARCHAR(3) DEFAULT 'USD',
    
    location VARCHAR(200),
    is_remote BOOLEAN DEFAULT false,
    openings INTEGER DEFAULT 1,
    
    status VARCHAR(20) DEFAULT 'draft', -- draft, published, closed, filled, cancelled
    published_at TIMESTAMPTZ,
    closes_at TIMESTAMPTZ,
    
    application_url VARCHAR(500),
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE job_postings IS 'Job openings and recruitment';

-- Job applications
CREATE TABLE job_applications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_posting_id UUID NOT NULL REFERENCES job_postings(id) ON DELETE CASCADE,
    
    applicant_name VARCHAR(200) NOT NULL,
    applicant_email CITEXT NOT NULL,
    applicant_phone VARCHAR(30),
    
    resume_url VARCHAR(500),
    cover_letter TEXT,
    portfolio_url VARCHAR(500),
    linkedin_url VARCHAR(500),
    
    current_company VARCHAR(200),
    current_designation VARCHAR(100),
    current_ctc DECIMAL(12,2),
    expected_ctc DECIMAL(12,2),
    notice_period_days INTEGER,
    
    status VARCHAR(20) DEFAULT 'applied', -- applied, screened, shortlisted, interviewed, offered, hired, rejected, withdrawn
    applied_at TIMESTAMPTZ DEFAULT now(),
    
    screened_by UUID REFERENCES users(id) ON DELETE SET NULL,
    screened_at TIMESTAMPTZ,
    interview_scheduled_at TIMESTAMPTZ,
    offer_sent_at TIMESTAMPTZ,
    joined_at TIMESTAMPTZ,
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE job_applications IS 'Applications for job postings';
CREATE INDEX idx_applications_posting ON job_applications(job_posting_id, status);
CREATE INDEX idx_applications_email ON job_applications(applicant_email);
CREATE INDEX idx_applications_status ON job_applications(status) WHERE status IN ('applied', 'screened', 'shortlisted');

-- Enable RLS
ALTER TABLE staff_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE leave_types ENABLE ROW LEVEL SECURITY;
ALTER TABLE leave_balances ENABLE ROW LEVEL SECURITY;
ALTER TABLE leave_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE leave_approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE contracts ENABLE ROW LEVEL SECURITY;
ALTER TABLE performance_reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_postings ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_applications ENABLE ROW LEVEL SECURITY;

-- RLS Policies
CREATE POLICY hr_tenant ON staff_profiles
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY leave_types_tenant ON leave_types
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY leave_balances_tenant ON leave_balances
    USING (user_id IN (SELECT id FROM users WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY leave_requests_tenant ON leave_requests
    USING (user_id IN (SELECT id FROM users WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY leave_approvals_tenant ON leave_approvals
    USING (leave_request_id IN (SELECT id FROM leave_requests WHERE user_id IN (SELECT id FROM users WHERE institute_id = current_setting('app.current_institute_id', true)::UUID)));

CREATE POLICY contracts_tenant ON contracts
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY performance_tenant ON performance_reviews
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY job_postings_tenant ON job_postings
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY job_applications_tenant ON job_applications
    USING (job_posting_id IN (SELECT id FROM job_postings WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

-- Self-access
CREATE POLICY leave_balances_self ON leave_balances
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY leave_requests_self ON leave_requests
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY contracts_self ON contracts
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY performance_self ON performance_reviews
    USING (employee_id = current_setting('app.current_user_id', true)::UUID);