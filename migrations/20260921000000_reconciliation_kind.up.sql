-- Payment reconciliations carry two kinds of durable Academic side effect:
--   * `activation` — the transaction was paid and the enrollment must become active.
--   * `release`    — the transaction failed or expired and the enrollment's seat must
--                    be returned to the catalog.
-- One row per transaction is kept (the existing unique index on transaction_id), so at
-- any moment the row holds the side effect that is still owed. A `release` row is only
-- rewritten back to `activation` by a late paid callback, which is exactly the case an
-- operator must be able to see: the seat was already released but the parent paid.
ALTER TABLE payment_reconciliations
    ADD COLUMN IF NOT EXISTS kind varchar(20) NOT NULL DEFAULT 'activation';

CREATE INDEX IF NOT EXISTS idx_payment_reconciliations_kind_due
    ON payment_reconciliations (kind, status, next_attempt_at);
