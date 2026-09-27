-- KEL-99: platform fee policy snapshot on every new transaction.
-- Transactions created before KEL-99 keep NULL here: they never had a policy
-- version and are not recomputed or backfilled.
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS platform_fee_policy_version bigint;
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS platform_fee_percent_bps bigint;
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS platform_fee_fixed bigint;

-- The snapshot is all-or-nothing and within the owner-approved bounds. Every
-- existing row is NULL/NULL/NULL, so validation cannot fail on old data. The
-- explicit IS NOT NULL guards matter: a CHECK that evaluates to NULL passes,
-- so without them a partial snapshot would be accepted.
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS chk_transactions_platform_fee_snapshot;
ALTER TABLE transactions ADD CONSTRAINT chk_transactions_platform_fee_snapshot CHECK (
    (platform_fee_policy_version IS NULL AND platform_fee_percent_bps IS NULL AND platform_fee_fixed IS NULL)
    OR (
        platform_fee_policy_version IS NOT NULL
        AND platform_fee_percent_bps IS NOT NULL
        AND platform_fee_fixed IS NOT NULL
        AND platform_fee_policy_version >= 0
        AND platform_fee_percent_bps BETWEEN 0 AND 2000
        AND platform_fee_fixed BETWEEN 0 AND 50000
        AND platform_fee = (gross_amount * platform_fee_percent_bps) / 10000 + platform_fee_fixed
        AND net_amount = gross_amount - platform_fee - payment_gateway_fee
        AND net_amount >= 0
    )
);

-- Once written, a snapshot and the amounts it produced never change, and a
-- legacy row can never be given a policy version afterwards. Writes that keep
-- the same values (for example a full-row save) are allowed.
CREATE OR REPLACE FUNCTION transactions_platform_fee_snapshot_immutable() RETURNS trigger AS $$
BEGIN
    IF OLD.platform_fee_policy_version IS DISTINCT FROM NEW.platform_fee_policy_version
        OR OLD.platform_fee_percent_bps IS DISTINCT FROM NEW.platform_fee_percent_bps
        OR OLD.platform_fee_fixed IS DISTINCT FROM NEW.platform_fee_fixed
        OR (OLD.platform_fee_policy_version IS NOT NULL AND (
            OLD.platform_fee IS DISTINCT FROM NEW.platform_fee
            OR OLD.gross_amount IS DISTINCT FROM NEW.gross_amount
            OR OLD.payment_gateway_fee IS DISTINCT FROM NEW.payment_gateway_fee
            OR OLD.net_amount IS DISTINCT FROM NEW.net_amount))
    THEN
        RAISE EXCEPTION 'platform fee snapshot of transaction % is immutable', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_transactions_platform_fee_snapshot_immutable ON transactions;
CREATE TRIGGER trg_transactions_platform_fee_snapshot_immutable
    BEFORE UPDATE ON transactions
    FOR EACH ROW EXECUTE FUNCTION transactions_platform_fee_snapshot_immutable();
