-- Maker-checker for imports (ONB-24): a file can need approval from someone other than its uploader before it is
-- imported. Whether it does is decided when the file is uploaded and kept on the batch, so a later change of the
-- institute's setting never alters a file already waiting.
ALTER TABLE import_batches
    ADD COLUMN approval_required boolean NOT NULL DEFAULT false,
    ADD COLUMN approved_by       uuid,
    ADD COLUMN approved_at       timestamptz,
    ADD COLUMN rejected_by       uuid,
    ADD COLUMN rejected_at       timestamptz,
    ADD COLUMN reject_reason     text CHECK (reject_reason IS NULL OR length(reject_reason) BETWEEN 1 AND 500);

ALTER TABLE import_batches DROP CONSTRAINT IF EXISTS import_batches_status_check;
ALTER TABLE import_batches ADD CONSTRAINT import_batches_status_check CHECK (status IN
    ('uploaded', 'validating', 'validated', 'committing', 'committed', 'rolling_back', 'rolled_back', 'failed', 'rejected'));

COMMENT ON COLUMN import_batches.approval_required IS 'Set at upload from the institute setting settings.importApprovalRequired (absent means required).';
