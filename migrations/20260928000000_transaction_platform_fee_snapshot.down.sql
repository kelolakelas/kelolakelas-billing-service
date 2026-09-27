-- Rolls back KEL-99's snapshot columns. This discards which policy version
-- priced each transaction; platform_fee and net_amount stay as charged.
DROP TRIGGER IF EXISTS trg_transactions_platform_fee_snapshot_immutable ON transactions;
DROP FUNCTION IF EXISTS transactions_platform_fee_snapshot_immutable();
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS chk_transactions_platform_fee_snapshot;
ALTER TABLE transactions DROP COLUMN IF EXISTS platform_fee_fixed;
ALTER TABLE transactions DROP COLUMN IF EXISTS platform_fee_percent_bps;
ALTER TABLE transactions DROP COLUMN IF EXISTS platform_fee_policy_version;
