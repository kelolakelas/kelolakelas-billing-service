-- KEL-59: transactions are looked up per enrollment (GetByEnrollmentID filters on
-- enrollment_id and returns the newest row by created_at; the transaction list
-- filters on enrollment_id and orders by created_at). Migration 000003 dropped the
-- earlier enrollment index when an enrollment started owning several transactions
-- (renewals, retries), so these lookups have been sequential scans since.
--
-- The index is deliberately NOT unique: one enrollment has many transactions. It
-- uses a new name so it never collides with idx_transactions_enrollment_id, which
-- migration 000002's down file still drops.
--
-- Built without CONCURRENTLY, like earlier migrations: writes to `transactions`
-- wait while it is built, which is acceptable at the current table size, and a
-- failed build can never leave an INVALID index that IF NOT EXISTS would then skip.
CREATE INDEX IF NOT EXISTS idx_transactions_enrollment_created_at
    ON transactions (enrollment_id, created_at);
