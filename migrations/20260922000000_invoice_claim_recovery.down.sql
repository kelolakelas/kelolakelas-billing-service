DROP INDEX IF EXISTS idx_transactions_stale_invoice_claim;
ALTER TABLE transactions DROP COLUMN IF EXISTS invoice_failure_reason;
ALTER TABLE transactions DROP COLUMN IF EXISTS invoice_claimed_at;
