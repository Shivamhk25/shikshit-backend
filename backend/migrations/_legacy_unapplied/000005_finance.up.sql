-- 000005_finance.up.sql
-- Finance module: fees, invoices, payments, payroll, expenses

-- Fee structures
CREATE TABLE fee_structures (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    academic_year_id UUID NOT NULL REFERENCES academic_years(id) ON DELETE CASCADE,
    name VARCHAR(200) NOT NULL,
    code VARCHAR(50) NOT NULL,
    description TEXT,
    applies_to_role user_role[],          -- Which roles this applies to
    grade_levels INTEGER[],               -- Specific grades
    departments UUID[],                   -- Specific departments
    is_active BOOLEAN DEFAULT true,
    is_default BOOLEAN DEFAULT false,
    settings JSONB DEFAULT '{}'::jsonb,   -- discounts, late fees, installment rules
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (institute_id, academic_year_id, code)
);

COMMENT ON TABLE fee_structures IS 'Fee structure definitions per academic year';

-- Fee components (tuition, transport, lab, etc.)
CREATE TABLE fee_components (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fee_structure_id UUID NOT NULL REFERENCES fee_structures(id) ON DELETE CASCADE,
    name VARCHAR(200) NOT NULL,
    code VARCHAR(50) NOT NULL,
    description TEXT,
    amount DECIMAL(12,2) NOT NULL,
    currency VARCHAR(3) DEFAULT 'USD',
    frequency VARCHAR(20) DEFAULT 'monthly', -- one_time, monthly, quarterly, semester, annually
    is_mandatory BOOLEAN DEFAULT true,
    is_refundable BOOLEAN DEFAULT false,
    tax_rate DECIMAL(5,4) DEFAULT 0,
    display_order INTEGER DEFAULT 0,
    settings JSONB DEFAULT '{}'::jsonb,   -- proration rules, sibling discounts
    UNIQUE (fee_structure_id, code)
);

COMMENT ON TABLE fee_components IS 'Individual fee items within a structure';

-- Student fee assignments
CREATE TABLE student_fees (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    student_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fee_structure_id UUID NOT NULL REFERENCES fee_structures(id) ON DELETE RESTRICT,
    academic_year_id UUID NOT NULL REFERENCES academic_years(id) ON DELETE CASCADE,
    
    -- Overrides
    discount_percent DECIMAL(5,2) DEFAULT 0,
    discount_amount DECIMAL(12,2) DEFAULT 0,
    discount_reason TEXT,
    waived_components UUID[],
    
    status VARCHAR(20) DEFAULT 'pending', -- pending, partial, paid, overdue, waived, cancelled
    total_amount DECIMAL(12,2) NOT NULL,
    paid_amount DECIMAL(12,2) DEFAULT 0,
    balance_amount DECIMAL(12,2) GENERATED ALWAYS AS (total_amount - paid_amount) STORED,
    
    due_date DATE,
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE student_fees IS 'Fee assignments per student per year';
CREATE INDEX idx_student_fees_student ON student_fees(student_id, status);
CREATE INDEX idx_student_fees_due ON student_fees(due_date, status) WHERE status IN ('pending', 'partial', 'overdue');

-- Invoices
CREATE TABLE invoices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    student_fee_id UUID REFERENCES student_fees(id) ON DELETE SET NULL,
    student_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    invoice_number VARCHAR(50) UNIQUE NOT NULL,
    type VARCHAR(20) DEFAULT 'fee', -- fee, transport, uniform, books, exam, fine, other
    
    subtotal DECIMAL(12,2) NOT NULL,
    tax_amount DECIMAL(12,2) DEFAULT 0,
    discount_amount DECIMAL(12,2) DEFAULT 0,
    total_amount DECIMAL(12,2) NOT NULL,
    currency VARCHAR(3) DEFAULT 'USD',
    
    status VARCHAR(20) DEFAULT 'draft', -- draft, sent, partial, paid, overdue, cancelled, refunded
    issued_at TIMESTAMPTZ DEFAULT now(),
    due_date DATE NOT NULL,
    paid_at TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ,
    cancelled_reason TEXT,
    
    notes TEXT,
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE invoices IS 'Invoices for fees and other charges';
CREATE INDEX idx_invoices_student ON invoices(student_id, status);
CREATE INDEX idx_invoices_due ON invoices(due_date, status) WHERE status IN ('sent', 'partial', 'overdue');
CREATE INDEX idx_invoices_number ON invoices(invoice_number);

-- Invoice line items
CREATE TABLE invoice_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    fee_component_id UUID REFERENCES fee_components(id) ON DELETE SET NULL,
    
    description VARCHAR(500) NOT NULL,
    quantity DECIMAL(8,2) DEFAULT 1,
    unit_price DECIMAL(12,2) NOT NULL,
    tax_rate DECIMAL(5,4) DEFAULT 0,
    discount_percent DECIMAL(5,2) DEFAULT 0,
    line_total DECIMAL(12,2) GENERATED ALWAYS AS (
        (unit_price * quantity) * (1 - discount_percent / 100) * (1 + tax_rate)
    ) STORED,
    display_order INTEGER DEFAULT 0
);

COMMENT ON TABLE invoice_items IS 'Line items within an invoice';

-- Payments
CREATE TABLE payments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    student_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invoice_id UUID REFERENCES invoices(id) ON DELETE SET NULL,
    
    payment_number VARCHAR(50) UNIQUE NOT NULL,
    amount DECIMAL(12,2) NOT NULL,
    currency VARCHAR(3) DEFAULT 'USD',
    
    method VARCHAR(30) NOT NULL,        -- cash, card, bank_transfer, upi, wallet, cheque, online
    reference VARCHAR(200),             -- Transaction ID, cheque number, etc.
    gateway VARCHAR(50),                -- stripe, razorpay, paypal, manual
    gateway_transaction_id VARCHAR(200),
    gateway_response JSONB,
    
    status VARCHAR(20) DEFAULT 'pending', -- pending, completed, failed, refunded, disputed
    paid_at TIMESTAMPTZ DEFAULT now(),
    settled_at TIMESTAMPTZ,
    failed_at TIMESTAMPTZ,
    failure_reason TEXT,
    
    receipt_number VARCHAR(50) UNIQUE,
    receipt_url VARCHAR(500),
    receipt_emailed BOOLEAN DEFAULT false,
    receipt_emailed_at TIMESTAMPTZ,
    
    notes TEXT,
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE payments IS 'Payment transactions';
CREATE INDEX idx_payments_student ON payments(student_id, status);
CREATE INDEX idx_payments_invoice ON payments(invoice_id);
CREATE INDEX idx_payments_status_date ON payments(status, paid_at);
CREATE INDEX idx_payments_gateway ON payments(gateway, gateway_transaction_id);

-- Payment allocations (which invoices a payment covers)
CREATE TABLE payment_allocations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id UUID NOT NULL REFERENCES payments(id) ON DELETE CASCADE,
    invoice_id UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    allocated_amount DECIMAL(12,2) NOT NULL,
    allocated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE payment_allocations IS 'Allocation of payments to invoices';

-- Refunds
CREATE TABLE refunds (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id UUID NOT NULL REFERENCES payments(id) ON DELETE CASCADE,
    invoice_id UUID REFERENCES invoices(id) ON DELETE SET NULL,
    
    refund_number VARCHAR(50) UNIQUE NOT NULL,
    amount DECIMAL(12,2) NOT NULL,
    reason VARCHAR(100) NOT NULL,       -- overpayment, cancellation, error, goodwill
    description TEXT,
    
    status VARCHAR(20) DEFAULT 'pending', -- pending, approved, processed, rejected
    approved_by UUID REFERENCES users(id) ON DELETE SET NULL,
    approved_at TIMESTAMPTZ,
    processed_by UUID REFERENCES users(id) ON DELETE SET NULL,
    processed_at TIMESTAMPTZ,
    rejected_by UUID REFERENCES users(id) ON DELETE SET NULL,
    rejected_at TIMESTAMPTZ,
    rejection_reason TEXT,
    
    gateway_refund_id VARCHAR(200),
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE refunds IS 'Refund requests and processing';

-- Expenses
CREATE TABLE expenses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    department_id UUID REFERENCES departments(id) ON DELETE SET NULL,
    submitted_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    approved_by UUID REFERENCES users(id) ON DELETE SET NULL,
    
    expense_number VARCHAR(50) UNIQUE NOT NULL,
    title VARCHAR(200) NOT NULL,
    description TEXT,
    category VARCHAR(50) NOT NULL,      -- supplies, equipment, maintenance, travel, utilities, other
    amount DECIMAL(12,2) NOT NULL,
    currency VARCHAR(3) DEFAULT 'USD',
    tax_amount DECIMAL(12,2) DEFAULT 0,
    total_amount DECIMAL(12,2) GENERATED ALWAYS AS (amount + tax_amount) STORED,
    
    expense_date DATE NOT NULL,
    receipt_urls TEXT[],
    
    status VARCHAR(20) DEFAULT 'draft', -- draft, submitted, approved, rejected, paid
    submitted_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    paid_at TIMESTAMPTZ,
    rejected_at TIMESTAMPTZ,
    rejection_reason TEXT,
    
    payment_method VARCHAR(30),
    payment_reference VARCHAR(200),
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE expenses IS 'Institute expenses and reimbursements';
CREATE INDEX idx_expenses_institute ON expenses(institute_id, status, expense_date);
CREATE INDEX idx_expenses_submitted_by ON expenses(submitted_by, status);

-- Payroll
CREATE TABLE payroll_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    period_start DATE NOT NULL,
    period_end DATE NOT NULL,
    pay_date DATE NOT NULL,
    
    status VARCHAR(20) DEFAULT 'draft', -- draft, processing, completed, cancelled
    total_gross DECIMAL(14,2) DEFAULT 0,
    total_deductions DECIMAL(14,2) DEFAULT 0,
    total_net DECIMAL(14,2) DEFAULT 0,
    employee_count INTEGER DEFAULT 0,
    
    processed_by UUID REFERENCES users(id) ON DELETE SET NULL,
    processed_at TIMESTAMPTZ,
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE payroll_runs IS 'Payroll processing runs';

CREATE TABLE payroll_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payroll_run_id UUID NOT NULL REFERENCES payroll_runs(id) ON DELETE CASCADE,
    employee_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    basic_salary DECIMAL(12,2) NOT NULL,
    allowances DECIMAL(12,2) DEFAULT 0,
    overtime_pay DECIMAL(12,2) DEFAULT 0,
    bonus DECIMAL(12,2) DEFAULT 0,
    gross_pay DECIMAL(12,2) GENERATED ALWAYS AS (basic_salary + allowances + overtime_pay + bonus) STORED,
    
    tax_deduction DECIMAL(12,2) DEFAULT 0,
    insurance_deduction DECIMAL(12,2) DEFAULT 0,
    retirement_deduction DECIMAL(12,2) DEFAULT 0,
    other_deductions DECIMAL(12,2) DEFAULT 0,
    total_deductions DECIMAL(12,2) GENERATED ALWAYS AS (tax_deduction + insurance_deduction + retirement_deduction + other_deductions) STORED,
    
    net_pay DECIMAL(12,2) GENERATED ALWAYS AS (gross_pay - total_deductions) STORED,
    
    payment_method VARCHAR(30),
    payment_reference VARCHAR(200),
    paid_at TIMESTAMPTZ,
    payslip_url VARCHAR(500),
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE payroll_entries IS 'Individual employee payroll entries';

-- Enable RLS
ALTER TABLE fee_structures ENABLE ROW LEVEL SECURITY;
ALTER TABLE fee_components ENABLE ROW LEVEL SECURITY;
ALTER TABLE student_fees ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoices ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoice_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_allocations ENABLE ROW LEVEL SECURITY;
ALTER TABLE refunds ENABLE ROW LEVEL SECURITY;
ALTER TABLE expenses ENABLE ROW LEVEL SECURITY;
ALTER TABLE payroll_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE payroll_entries ENABLE ROW LEVEL SECURITY;

-- RLS Policies
CREATE POLICY finance_tenant ON fee_structures
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY fee_components_tenant ON fee_components
    USING (fee_structure_id IN (SELECT id FROM fee_structures WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY student_fees_tenant ON student_fees
    USING (student_id IN (SELECT id FROM users WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY invoices_tenant ON invoices
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY payments_tenant ON payments
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY refunds_tenant ON refunds
    USING (payment_id IN (SELECT id FROM payments WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY expenses_tenant ON expenses
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY payroll_runs_tenant ON payroll_runs
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY payroll_entries_tenant ON payroll_entries
    USING (payroll_run_id IN (SELECT id FROM payroll_runs WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

-- Self-access
CREATE POLICY student_fees_self ON student_fees
    USING (student_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY invoices_self ON invoices
    USING (student_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY payments_self ON payments
    USING (student_id = current_setting('app.current_user_id', true)::UUID);