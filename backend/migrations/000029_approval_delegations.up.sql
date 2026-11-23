-- 000029_approval_delegations: a person who may decide approvals hands them over to another user for a period
-- (FND-20 slice 2, approval A-17). A delegation is data, never a flag on the user: whether it applies is decided by
-- the clock at the moment of a decision (starts_at <= now < ends_at, not ended early). The 90-day cap is a rule in
-- code, not here, so the founder can change it without a migration.

CREATE TABLE approval_delegations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    delegator_id uuid NOT NULL,
    delegate_id  uuid NOT NULL,
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz NOT NULL,
    reason       text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 300),
    -- NULL means every workflow type; otherwise only these types
    type_keys    text[] CHECK (type_keys IS NULL OR cardinality(type_keys) BETWEEN 1 AND 50),
    -- set when the delegator ends it before ends_at
    ended_at     timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (delegator_id <> delegate_id),
    CHECK (ends_at > starts_at),
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, delegator_id) REFERENCES users (tenant_id, id),
    FOREIGN KEY (tenant_id, delegate_id) REFERENCES users (tenant_id, id)
);
COMMENT ON TABLE approval_delegations IS 'A user handing their approval rights to another user for a period (FND-20). Active when starts_at <= now < ends_at and ended_at IS NULL, checked at decision time.';
CREATE INDEX approval_delegations_delegate_idx ON approval_delegations (tenant_id, delegate_id, ends_at DESC);
CREATE INDEX approval_delegations_delegator_idx ON approval_delegations (tenant_id, delegator_id, created_at DESC);

-- On a decision made through a delegation: the person the decider acted for.
ALTER TABLE approval_events ADD COLUMN on_behalf_of uuid;
ALTER TABLE approval_events ADD FOREIGN KEY (tenant_id, on_behalf_of) REFERENCES users (tenant_id, id);

ALTER TABLE approval_delegations ENABLE ROW LEVEL SECURITY;
CREATE POLICY approval_delegations_tenant ON approval_delegations USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
