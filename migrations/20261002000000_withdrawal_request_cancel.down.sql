DROP INDEX IF EXISTS idx_withdrawals_tenant_status;
DROP INDEX IF EXISTS uq_withdrawals_tenant_idempotency;

ALTER TABLE withdrawals
    DROP COLUMN IF EXISTS cancelled_at,
    DROP COLUMN IF EXISTS account_name_snapshot,
    DROP COLUMN IF EXISTS account_number_snapshot,
    DROP COLUMN IF EXISTS bank_code_snapshot,
    DROP COLUMN IF EXISTS idempotency_key;
