UPDATE import_batches SET status = 'failed', failure = COALESCE(failure, 'Rejected: ' || COALESCE(reject_reason, '')) WHERE status = 'rejected';
ALTER TABLE import_batches DROP CONSTRAINT IF EXISTS import_batches_status_check;
ALTER TABLE import_batches ADD CONSTRAINT import_batches_status_check CHECK (status IN
    ('uploaded', 'validating', 'validated', 'committing', 'committed', 'rolling_back', 'rolled_back', 'failed'));
ALTER TABLE import_batches
    DROP COLUMN reject_reason, DROP COLUMN rejected_at, DROP COLUMN rejected_by,
    DROP COLUMN approved_at, DROP COLUMN approved_by, DROP COLUMN approval_required;
