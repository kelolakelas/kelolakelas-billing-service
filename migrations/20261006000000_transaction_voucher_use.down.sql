DROP TRIGGER IF EXISTS transaction_voucher_accounting ON transactions;
DROP FUNCTION IF EXISTS transaction_voucher_accounting();
ALTER TABLE transactions DROP COLUMN voucher_use_claimed_at;
ALTER TABLE transactions DROP COLUMN voucher_use_released_at;
