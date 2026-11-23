-- Year rollover (TEN-01): setting up the next academic year from the one that is ending. The rollover copies the
-- year-scoped structure (terms, class-section mappings, subject-class mappings, holidays, calendar events) and can make
-- the new year current. Each step runs in its own transaction and leaves one row here, so the screen can show what was
-- done, by whom and what it changed. The log is append-only: a failed attempt is recorded too.
--
-- This adds the institute-configuration module (ten) to the catalog and to the plans, with the three permissions the
-- rollover uses: view (checklist and history), create (dry-run and run the copy steps, create the new year) and edit
-- (make the new year current). Only the Admin template reaches them (its *.*.* grant); nobody else gets them by default.

INSERT INTO permissions (code, module, resource, action, sensitive)
SELECT 'ten.year_rollover.' || a, 'ten', 'year_rollover', a, false
FROM unnest(ARRAY['view', 'create', 'edit']) AS a
ON CONFLICT (code) DO NOTHING;

UPDATE plans SET modules = array_append(modules, 'ten') WHERE NOT ('ten' = ANY (modules));

-- Institutes that already exist get the module their plan now includes. The tenant_modules trigger bumps their users'
-- access version, so the new screen shows up without a sign-in.
INSERT INTO tenant_modules (tenant_id, module_code)
SELECT t.id, 'ten' FROM tenants t JOIN plans p ON p.id = t.plan_id
WHERE 'ten' = ANY (p.modules)
ON CONFLICT (tenant_id, module_code) DO NOTHING;

CREATE TABLE year_rollover_runs (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    from_year_id   uuid NOT NULL,
    to_year_id     uuid NOT NULL,
    step           text NOT NULL CHECK (step IN ('terms', 'class_sections', 'subject_classes', 'holidays', 'calendar_events', 'make_current')),
    status         text NOT NULL CHECK (status IN ('completed', 'failed')),
    created_count  integer NOT NULL DEFAULT 0 CHECK (created_count >= 0),
    existing_count integer NOT NULL DEFAULT 0 CHECK (existing_count >= 0),
    skipped_count  integer NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    conflict_count integer NOT NULL DEFAULT 0 CHECK (conflict_count >= 0),
    review_count   integer NOT NULL DEFAULT 0 CHECK (review_count >= 0),
    warnings       jsonb NOT NULL DEFAULT '[]'::jsonb,
    error          text CHECK (error IS NULL OR length(error) <= 500),
    run_by         uuid,
    run_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, from_year_id) REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, to_year_id)   REFERENCES academic_years (tenant_id, id),
    CHECK (from_year_id <> to_year_id),
    CHECK ((status = 'failed') = (error IS NOT NULL))
);
COMMENT ON TABLE year_rollover_runs IS 'One row per executed rollover step (TEN-01): what it copied, skipped and refused. Append-only; dry-runs are not logged.';
CREATE INDEX year_rollover_runs_list_idx ON year_rollover_runs (tenant_id, run_at DESC);
CREATE INDEX year_rollover_runs_pair_idx ON year_rollover_runs (tenant_id, from_year_id, to_year_id, step, run_at DESC);
ALTER TABLE year_rollover_runs ENABLE ROW LEVEL SECURITY;
CREATE POLICY year_rollover_runs_tenant ON year_rollover_runs USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
REVOKE UPDATE, DELETE, TRUNCATE ON year_rollover_runs FROM shikshit_app;
