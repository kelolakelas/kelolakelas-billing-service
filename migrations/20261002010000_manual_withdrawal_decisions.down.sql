DROP INDEX IF EXISTS idx_withdrawals_requested_queue;
DROP INDEX IF EXISTS uq_withdrawals_transfer_reference;
ALTER TABLE withdrawals DROP COLUMN IF EXISTS reject_reason, DROP COLUMN IF EXISTS transfer_reference,
    DROP COLUMN IF EXISTS decided_at, DROP COLUMN IF EXISTS decided_by;
