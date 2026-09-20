-- Invoice creation is claimed by moving a transaction to `creating` before the provider
-- is called, which is what makes two parallel requests produce exactly one invoice. That
-- claim needs two extra columns so it can always be taken back:
--
--   * invoice_claimed_at       — when the row entered `creating`. A claim whose process
--                                died mid-call is only reclaimable once this timestamp is
--                                older than the configured claim timeout, so a slow
--                                provider is never duplicated by an impatient retry.
--   * invoice_failure_reason   — why the last invoice creation failed. The retry history
--                                is what an operator needs to tell a provider outage apart
--                                from a rejected request.
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS invoice_claimed_at timestamp;
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS invoice_failure_reason text;

-- Rows already stuck in `creating` predate the column. They are the exact defect this
-- migration repairs, so they are backfilled from `updated_at` — the moment the claim was
-- written — which makes them reclaimable after the timeout instead of instantly. A NULL
-- would otherwise have to be treated as ancient, and a NULL claim time on a live row
-- would let a concurrent retry duplicate an in-flight invoice.
UPDATE transactions
SET invoice_claimed_at = updated_at
WHERE status = 'creating'
  AND invoice_claimed_at IS NULL;

-- The stale-claim reclaim looks up `creating` rows by claim age.
CREATE INDEX IF NOT EXISTS idx_transactions_stale_invoice_claim
    ON transactions (invoice_claimed_at)
    WHERE status = 'creating';
