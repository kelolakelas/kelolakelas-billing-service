DROP INDEX IF EXISTS idx_payment_reconciliations_kind_due;
ALTER TABLE payment_reconciliations DROP COLUMN IF EXISTS kind;
