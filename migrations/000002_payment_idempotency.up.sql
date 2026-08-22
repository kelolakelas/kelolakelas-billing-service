CREATE UNIQUE INDEX IF NOT EXISTS idx_transactions_enrollment_id
    ON transactions (enrollment_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_enrollment_id
    ON subscriptions (enrollment_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ledger_payment_event
    ON ledger_entries (reference_id, reference_type, entry_type);