-- KEL-144: immutable decision evidence for manually processed withdrawals.
ALTER TABLE withdrawals
    ADD COLUMN IF NOT EXISTS decided_by uuid,
    ADD COLUMN IF NOT EXISTS decided_at timestamp,
    ADD COLUMN IF NOT EXISTS transfer_reference varchar(255),
    ADD COLUMN IF NOT EXISTS reject_reason text;
CREATE UNIQUE INDEX IF NOT EXISTS uq_withdrawals_transfer_reference
    ON withdrawals (transfer_reference) WHERE transfer_reference IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_withdrawals_requested_queue
    ON withdrawals (requested_at, id) WHERE status = 'requested';
