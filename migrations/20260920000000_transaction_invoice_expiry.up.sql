-- Store the requested Duitku invoice expiry so unpaid transactions reach a final state.
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS invoice_expires_at timestamp;
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS expired_at timestamp;

-- Backfill historical pending rows with the longest configured invoice validity
-- (14 days) so a possibly-payable invoice is never expired early. A paid callback
-- that arrives after local expiry is still accepted by the callback handler.
UPDATE transactions
SET invoice_expires_at = created_at + interval '14 days'
WHERE status = 'pending'
  AND invoice_expires_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_transactions_due_expiry
    ON transactions (invoice_expires_at)
    WHERE status = 'pending' AND invoice_expires_at IS NOT NULL;
