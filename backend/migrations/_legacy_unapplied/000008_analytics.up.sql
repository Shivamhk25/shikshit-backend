-- 000008_analytics.up.sql
-- Analytics module: events, dashboards, reports, AI insights

-- Event tracking (for analytics and AI)
CREATE TABLE analytics_events (
    id BIGSERIAL PRIMARY KEY,
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    session_id UUID,
    
    event_name VARCHAR(100) NOT NULL,   -- page_view, button_click, assignment_submit, grade_view, etc.
    event_category VARCHAR(50),         -- navigation, academic, finance, communication, settings
    event_action VARCHAR(50),           -- view, click, submit, download, share, search
    event_label VARCHAR(200),
    event_value DECIMAL(12,2),
    
    -- Context
    page_url VARCHAR(500),
    page_title VARCHAR(300),
    referrer VARCHAR(500),
    user_agent TEXT,
    ip_address INET,
    country VARCHAR(2),
    region VARCHAR(100),
    city VARCHAR(100),
    device_type VARCHAR(20),            -- desktop, mobile, tablet
    browser VARCHAR(50),
    os VARCHAR(50),
    
    -- Properties (flexible)
    properties JSONB DEFAULT '{}'::jsonb,
    
    -- Timing
    timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    
    -- Partitioning by month (handled by TimescaleDB)
    UNIQUE (institute_id, id)
);

COMMENT ON TABLE analytics_events IS 'Raw analytics events for dashboards and AI';
CREATE INDEX idx_analytics_institute_time ON analytics_events(institute_id, timestamp DESC);
CREATE INDEX idx_analytics_user_time ON analytics_events(user_id, timestamp DESC);
CREATE INDEX idx_analytics_event_name ON analytics_events(event_name, timestamp DESC);
CREATE INDEX idx_analytics_session ON analytics_events(session_id, timestamp);

-- Convert to hypertable (TimescaleDB)
-- SELECT create_hypertable('analytics_events', 'timestamp', 
--     chunk_time_interval => INTERVAL '1 day',
--     if_not_exists => TRUE);

-- Dashboards
CREATE TABLE dashboards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    
    name VARCHAR(200) NOT NULL,
    description TEXT,
    role_target user_role[],            -- Which roles see this dashboard
    is_default BOOLEAN DEFAULT false,
    is_system BOOLEAN DEFAULT false,    -- Built-in, non-deletable
    layout JSONB DEFAULT '{}'::jsonb,   -- Grid layout config
    settings JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE dashboards IS 'Customizable dashboards per role';

-- Dashboard widgets
CREATE TABLE dashboard_widgets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dashboard_id UUID NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
    
    type VARCHAR(50) NOT NULL,          -- metric, chart, table, list, progress, activity, calendar
    title VARCHAR(200) NOT NULL,
    description TEXT,
    
    -- Data source
    data_source VARCHAR(100) NOT NULL,  -- enrollment_trend, attendance_rate, fee_collection, grade_distribution, etc.
    query_config JSONB DEFAULT '{}'::jsonb, -- Filters, group by, time range, etc.
    
    -- Visualization
    chart_type VARCHAR(30),             -- line, bar, pie, donut, area, scatter, heatmap, gauge
    chart_config JSONB DEFAULT '{}'::jsonb, -- Colors, axes, legends, tooltips
    
    -- Layout
    x INTEGER DEFAULT 0,
    y INTEGER DEFAULT 0,
    width INTEGER DEFAULT 6,
    height INTEGER DEFAULT 4,
    min_width INTEGER DEFAULT 3,
    min_height INTEGER DEFAULT 2,
    
    -- Refresh
    refresh_interval INTEGER DEFAULT 300, -- seconds, 0 = manual
    cache_ttl INTEGER DEFAULT 60,
    
    -- Permissions
    visible_roles user_role[],
    
    is_active BOOLEAN DEFAULT true,
    display_order INTEGER DEFAULT 0,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE dashboard_widgets IS 'Widgets on dashboards';
COMMENT ON COLUMN dashboard_widgets.data_source IS 'Pre-defined data source key';
COMMENT ON COLUMN dashboard_widgets.query_config IS 'Runtime query parameters';
CREATE INDEX idx_widgets_dashboard ON dashboard_widgets(dashboard_id, display_order);

-- Reports
CREATE TABLE reports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    
    name VARCHAR(200) NOT NULL,
    description TEXT,
    category VARCHAR(50),               -- academic, attendance, finance, hr, compliance, custom
    
    -- Template
    template_type VARCHAR(30),          -- pdf, excel, csv, html
    template_config JSONB DEFAULT '{}'::jsonb, -- Layout, sections, styling
    sql_query TEXT,                     -- For custom reports
    parameters_schema JSONB DEFAULT '{}'::jsonb, -- Parameter definitions
    
    -- Scheduling
    schedule_cron VARCHAR(100),         -- Cron expression for auto-generation
    schedule_timezone VARCHAR(50) DEFAULT 'UTC',
    last_run_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ,
    
    -- Distribution
    recipients UUID[],                  -- User IDs
    recipient_roles user_role[],
    delivery_channels VARCHAR(20)[],    -- email, in_app, s3, ftp
    
    -- Status
    is_active BOOLEAN DEFAULT true,
    is_system BOOLEAN DEFAULT false,
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE reports IS 'Scheduled and on-demand reports';
CREATE INDEX idx_reports_institute ON reports(institute_id, is_active);

-- Report executions
CREATE TABLE report_executions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id UUID NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    triggered_by UUID REFERENCES users(id) ON DELETE SET NULL,
    triggered_at TIMESTAMPTZ DEFAULT now(),
    
    status VARCHAR(20) DEFAULT 'pending', -- pending, running, completed, failed, cancelled
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    
    output_format VARCHAR(20),
    output_url VARCHAR(500),
    output_size_bytes BIGINT,
    row_count BIGINT,
    
    error_message TEXT,
    metadata JSONB DEFAULT '{}'::jsonb
);

COMMENT ON TABLE report_executions IS 'Report generation history';
CREATE INDEX idx_report_executions_report ON report_executions(report_id, triggered_at DESC);

-- AI Insights
CREATE TABLE ai_insights (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    
    insight_type VARCHAR(50) NOT NULL,  -- risk_alert, trend_analysis, recommendation, anomaly, prediction
    severity VARCHAR(20) DEFAULT 'info', -- info, warning, critical
    
    title VARCHAR(300) NOT NULL,
    description TEXT,
    recommendation TEXT,
    
    -- Affected entities
    affected_students UUID[],
    affected_staff UUID[],
    affected_classes UUID[],
    affected_departments UUID[],
    
    -- Confidence and evidence
    confidence_score DECIMAL(3,2),      -- 0.00 - 1.00
    evidence JSONB DEFAULT '{}'::jsonb, -- Supporting data points
    model_version VARCHAR(50),
    
    -- Status
    status VARCHAR(20) DEFAULT 'active', -- active, acknowledged, dismissed, resolved
    acknowledged_by UUID REFERENCES users(id) ON DELETE SET NULL,
    acknowledged_at TIMESTAMPTZ,
    dismissed_by UUID REFERENCES users(id) ON DELETE SET NULL,
    dismissed_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    
    -- Validity
    valid_from TIMESTAMPTZ DEFAULT now(),
    valid_until TIMESTAMPTZ,
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE ai_insights IS 'AI-generated insights and alerts';
CREATE INDEX idx_ai_insights_institute ON ai_insights(institute_id, status, created_at DESC);
CREATE INDEX idx_ai_insights_severity ON ai_insights(severity, status) WHERE status = 'active';
CREATE INDEX idx_ai_insights_type ON ai_insights(insight_type, created_at DESC);

-- Insight feedback (for model improvement)
CREATE TABLE insight_feedback (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    insight_id UUID NOT NULL REFERENCES ai_insights(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    rating INTEGER NOT NULL,            -- 1-5 (helpful)
    feedback_text TEXT,
    action_taken VARCHAR(50),           -- investigated, intervened, ignored, false_positive
    
    created_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE insight_feedback IS 'Feedback on AI insights for model training';
CREATE INDEX idx_insight_feedback_insight ON insight_feedback(insight_id);

-- KPI Definitions
CREATE TABLE kpi_definitions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    
    code VARCHAR(50) UNIQUE NOT NULL,
    name VARCHAR(200) NOT NULL,
    description TEXT,
    category VARCHAR(50),               -- academic, attendance, finance, hr, engagement
    
    -- Calculation
    formula TEXT NOT NULL,              -- SQL or expression
    unit VARCHAR(30),                   -- percentage, count, currency, days
    decimal_places INTEGER DEFAULT 2,
    
    -- Targets
    target_value DECIMAL(12,4),
    target_direction VARCHAR(20) DEFAULT 'higher', -- higher, lower, maintain
    warning_threshold DECIMAL(12,4),
    critical_threshold DECIMAL(12,4),
    
    -- Visualization
    chart_type VARCHAR(30) DEFAULT 'line',
    color VARCHAR(7) DEFAULT '#4682b4',
    
    is_active BOOLEAN DEFAULT true,
    is_system BOOLEAN DEFAULT false,
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE kpi_definitions IS 'Key Performance Indicator definitions';
COMMENT ON COLUMN kpi_definitions.formula IS 'SQL expression or reference to materialized view';

-- KPI Values (computed periodically)
CREATE TABLE kpi_values (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kpi_id UUID NOT NULL REFERENCES kpi_definitions(id) ON DELETE CASCADE,
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    
    period_start DATE NOT NULL,
    period_end DATE NOT NULL,
    period_type VARCHAR(20) DEFAULT 'monthly', -- daily, weekly, monthly, quarterly, yearly
    
    value DECIMAL(12,4) NOT NULL,
    target_value DECIMAL(12,4),
    variance DECIMAL(12,4) GENERATED ALWAYS AS (value - target_value) STORED,
    variance_percent DECIMAL(8,4) GENERATED ALWAYS AS (
        CASE WHEN target_value != 0 
        THEN ROUND(((value - target_value) / target_value) * 100, 4)
        ELSE NULL END
    ) STORED,
    
    status VARCHAR(20) GENERATED ALWAYS AS (
        CASE 
            WHEN critical_threshold IS NOT NULL AND 
                 ((target_direction = 'higher' AND value <= critical_threshold) OR
                  (target_direction = 'lower' AND value >= critical_threshold))
            THEN 'critical'
            WHEN warning_threshold IS NOT NULL AND
                 ((target_direction = 'higher' AND value <= warning_threshold) OR
                  (target_direction = 'lower' AND value >= warning_threshold))
            THEN 'warning'
            WHEN target_value IS NOT NULL AND
                 ((target_direction = 'higher' AND value >= target_value) OR
                  (target_direction = 'lower' AND value <= target_value))
            THEN 'on_track'
            ELSE 'no_target'
        END
    ) STORED,
    
    computed_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (kpi_id, period_start, period_end)
);

COMMENT ON TABLE kpi_values IS 'Computed KPI values over time';
CREATE INDEX idx_kpi_values_kpi_period ON kpi_values(kpi_id, period_start DESC);
CREATE INDEX idx_kpi_values_institute_period ON kpi_values(institute_id, period_start DESC);

-- Enable RLS
ALTER TABLE analytics_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE dashboards ENABLE ROW LEVEL SECURITY;
ALTER TABLE dashboard_widgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_executions ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_insights ENABLE ROW LEVEL SECURITY;
ALTER TABLE insight_feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE kpi_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE kpi_values ENABLE ROW LEVEL SECURITY;

-- RLS Policies
CREATE POLICY analytics_tenant ON analytics_events
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY dashboards_tenant ON dashboards
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY widgets_tenant ON dashboard_widgets
    USING (dashboard_id IN (SELECT id FROM dashboards WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY reports_tenant ON reports
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY report_executions_tenant ON report_executions
    USING (report_id IN (SELECT id FROM reports WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY ai_insights_tenant ON ai_insights
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY insight_feedback_tenant ON insight_feedback
    USING (insight_id IN (SELECT id FROM ai_insights WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY kpi_definitions_tenant ON kpi_definitions
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY kpi_values_tenant ON kpi_values
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

-- Role-based dashboard access
CREATE POLICY dashboards_role ON dashboards
    USING (
        'super_admin' = ANY(role_target)
        OR current_setting('app.current_role', true) = ANY(role_target)
        OR role_target IS NULL
        OR array_length(role_target, 1) IS NULL
    );