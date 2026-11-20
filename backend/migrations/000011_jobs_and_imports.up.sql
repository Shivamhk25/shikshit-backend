-- 000011_jobs_and_imports: a small Postgres-backed job queue (FND-18, minimum) and the staging tables of the import
-- engine (ONB-05). Rows are institute-scoped by row-level security; the worker claims jobs across institutes as the
-- platform tier and then runs each job inside its own institute.

CREATE TABLE jobs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid REFERENCES tenants(id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (length(kind) BETWEEN 1 AND 100),
    payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    attempts     integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 20),
    run_after    timestamptz NOT NULL DEFAULT now(),
    locked_at    timestamptz,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
COMMENT ON TABLE jobs IS 'Background jobs. Claimed with FOR UPDATE SKIP LOCKED; a job stuck in running is requeued after the lock times out.';
CREATE INDEX jobs_claim_idx ON jobs (run_after) WHERE status = 'queued';
CREATE INDEX jobs_running_idx ON jobs (locked_at) WHERE status = 'running';
ALTER TABLE jobs ENABLE ROW LEVEL SECURITY;
CREATE POLICY jobs_tenant ON jobs USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
CREATE POLICY jobs_platform ON jobs FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

CREATE TABLE import_batches (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    entity             text NOT NULL,
    strategy           text NOT NULL CHECK (strategy IN ('insert', 'update', 'upsert')),
    commit_mode        text NOT NULL CHECK (commit_mode IN ('all_or_nothing', 'skip_invalid')),
    status             text NOT NULL DEFAULT 'uploaded' CHECK (status IN
                         ('uploaded', 'validating', 'validated', 'committing', 'committed', 'rolling_back', 'rolled_back', 'failed')),
    file_name          text NOT NULL,
    template_version   integer NOT NULL,
    total_rows         integer NOT NULL DEFAULT 0,
    valid_rows         integer NOT NULL DEFAULT 0,
    warning_rows       integer NOT NULL DEFAULT 0,
    error_rows         integer NOT NULL DEFAULT 0,
    committed_rows     integer NOT NULL DEFAULT 0,
    reverted_rows      integer NOT NULL DEFAULT 0,
    progress_done      integer NOT NULL DEFAULT 0,
    progress_total     integer NOT NULL DEFAULT 0,
    failure            text,
    created_by         uuid,
    created_at         timestamptz NOT NULL DEFAULT now(),
    validated_at       timestamptz,
    committed_at       timestamptz,
    rollback_deadline  timestamptz,
    rolled_back_at     timestamptz,
    expires_at         timestamptz NOT NULL,
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE import_batches IS 'One uploaded file and its lifecycle (ONB-05). expires_at is the retention limit for the staged rows.';
CREATE INDEX import_batches_tenant_idx ON import_batches (tenant_id, created_at DESC);
CREATE INDEX import_batches_expiry_idx ON import_batches (expires_at);
ALTER TABLE import_batches ENABLE ROW LEVEL SECURITY;
CREATE POLICY import_batches_tenant ON import_batches USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
CREATE POLICY import_batches_platform ON import_batches FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

CREATE TABLE import_rows (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id        uuid NOT NULL,
    batch_id         uuid NOT NULL,
    row_no           integer NOT NULL,
    raw              jsonb NOT NULL,
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN
                       ('pending', 'valid', 'warning', 'error', 'committed', 'skipped', 'reverted', 'revert_failed')),
    action           text CHECK (action IN ('create', 'update')),
    record_id        uuid,
    before_snapshot  jsonb,
    after_updated_at timestamptz,
    FOREIGN KEY (tenant_id, batch_id) REFERENCES import_batches (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (batch_id, row_no)
);
COMMENT ON TABLE import_rows IS 'Staged rows. before_snapshot holds the previous state of an updated record so a rollback can restore it.';
CREATE INDEX import_rows_status_idx ON import_rows (batch_id, status, row_no);
ALTER TABLE import_rows ENABLE ROW LEVEL SECURITY;
CREATE POLICY import_rows_tenant ON import_rows USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
CREATE POLICY import_rows_platform ON import_rows FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());

CREATE TABLE import_errors (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id  uuid NOT NULL,
    batch_id   uuid NOT NULL,
    row_no     integer NOT NULL,
    column_key text,
    severity   text NOT NULL DEFAULT 'error' CHECK (severity IN ('error', 'warning')),
    code       text NOT NULL,
    message    text NOT NULL,
    FOREIGN KEY (tenant_id, batch_id) REFERENCES import_batches (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX import_errors_batch_idx ON import_errors (batch_id, row_no);
ALTER TABLE import_errors ENABLE ROW LEVEL SECURITY;
CREATE POLICY import_errors_tenant ON import_errors USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
CREATE POLICY import_errors_platform ON import_errors FOR ALL USING (app_is_platform()) WITH CHECK (app_is_platform());
