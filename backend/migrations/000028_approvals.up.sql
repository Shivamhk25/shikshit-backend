-- 000028_approvals: generic approval requests (FND-20). A module registers a workflow type in Go; each request is one
-- thing waiting for a decision (the subject), with its ordered steps and an append-only history. Every table is
-- institute-scoped with row-level security and composite tenant foreign keys.

CREATE TABLE approval_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    type_key     text NOT NULL CHECK (type_key ~ '^[a-z][a-z0-9_.]{0,79}$'),
    subject_id   text NOT NULL CHECK (char_length(subject_id) BETWEEN 1 AND 100),
    summary      text NOT NULL DEFAULT '' CHECK (char_length(summary) <= 300),
    amount       numeric(14,2) CHECK (amount IS NULL OR amount >= 0),
    -- the record the request is about, for scoped approvers (for example class_section / <mapping id>)
    scope_type   text,
    scope_ref    text,
    requester_id uuid NOT NULL,
    status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    current_step integer NOT NULL CHECK (current_step >= 1),
    step_count   integer NOT NULL CHECK (step_count >= 1 AND current_step <= step_count),
    created_at   timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz,
    CHECK ((scope_type IS NULL) = (scope_ref IS NULL)),
    CHECK ((status = 'pending') = (decided_at IS NULL)),
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, requester_id) REFERENCES users (tenant_id, id)
);
COMMENT ON TABLE approval_requests IS 'One approval request (FND-20). subject_id is the id of the record in the owning module; type_key names the registered workflow type.';
-- A subject has at most one open request per type; this also settles two simultaneous submissions.
CREATE UNIQUE INDEX approval_requests_open_uq ON approval_requests (tenant_id, type_key, subject_id) WHERE status = 'pending';
CREATE INDEX approval_requests_requester_idx ON approval_requests (tenant_id, requester_id, created_at DESC, id DESC);
CREATE INDEX approval_requests_pending_idx ON approval_requests (tenant_id, created_at, id) WHERE status = 'pending';

CREATE TABLE approval_steps (
    tenant_id  uuid NOT NULL,
    request_id uuid NOT NULL,
    step_no    integer NOT NULL CHECK (step_no >= 1),
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    -- copied from the workflow type when the request is made, so changing the type later does not change open requests
    permission text NOT NULL CHECK (char_length(permission) BETWEEN 1 AND 120),
    status     text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    decided_by uuid,
    decided_at timestamptz,
    PRIMARY KEY (request_id, step_no),
    FOREIGN KEY (tenant_id, request_id) REFERENCES approval_requests (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, decided_by) REFERENCES users (tenant_id, id)
);

CREATE TABLE approval_events (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    request_id uuid NOT NULL,
    step_no    integer,
    actor_id   uuid NOT NULL,
    action     text NOT NULL CHECK (action IN ('submitted', 'approved', 'rejected', 'cancelled')),
    comment    text NOT NULL DEFAULT '' CHECK (char_length(comment) <= 1000),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, request_id) REFERENCES approval_requests (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, actor_id) REFERENCES users (tenant_id, id)
);
CREATE INDEX approval_events_request_idx ON approval_events (request_id, created_at, id);

ALTER TABLE approval_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_steps ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY approval_requests_tenant ON approval_requests USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
CREATE POLICY approval_steps_tenant ON approval_steps USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
CREATE POLICY approval_events_tenant ON approval_events USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
