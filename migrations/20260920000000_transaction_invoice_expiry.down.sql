DROP INDEX IF EXISTS idx_transactions_due_expiry;
ALTER TABLE transactions DROP COLUMN IF EXISTS expired_at;
ALTER TABLE transactions DROP COLUMN IF EXISTS invoice_expires_at;
