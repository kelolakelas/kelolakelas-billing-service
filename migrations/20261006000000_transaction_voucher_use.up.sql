-- Reservation accounting follows status in the same database transaction,
-- including expiry/cancellation batch statements and callback saves.
ALTER TABLE transactions ADD COLUMN voucher_use_released_at TIMESTAMP;
ALTER TABLE transactions ADD COLUMN voucher_use_claimed_at TIMESTAMP;
-- Historical terminal voucher payments no longer hold a reservation.
UPDATE transactions SET voucher_use_released_at = now() WHERE voucher_id IS NOT NULL AND status IN ('expired','cancelled','failed');
CREATE FUNCTION transaction_voucher_accounting() RETURNS trigger AS $$
DECLARE v vouchers%ROWTYPE;
BEGIN
 IF (OLD.voucher_id IS NOT NULL OR NEW.voucher_id IS NOT NULL) AND (NEW.voucher_id IS DISTINCT FROM OLD.voucher_id OR NEW.subtotal_amount IS DISTINCT FROM OLD.subtotal_amount OR NEW.discount_amount IS DISTINCT FROM OLD.discount_amount) THEN
  RAISE EXCEPTION 'transaction voucher snapshot is immutable';
 END IF;
 NEW.voucher_use_released_at := OLD.voucher_use_released_at;
 NEW.voucher_use_claimed_at := OLD.voucher_use_claimed_at;
 IF NEW.voucher_id IS NULL THEN RETURN NEW; END IF;
 IF NEW.status IN ('expired','cancelled','failed') AND OLD.voucher_use_released_at IS NULL THEN
  UPDATE vouchers SET current_uses = current_uses - 1, updated_at = now() WHERE id = NEW.voucher_id AND current_uses > 0;
  NEW.voucher_use_released_at := now();
 ELSIF NEW.status IN ('creating','pending','paid') AND OLD.voucher_use_released_at IS NOT NULL THEN
  SELECT * INTO v FROM vouchers WHERE id = NEW.voucher_id FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'voucher_rejected'; END IF;
  IF NEW.status <> 'paid' AND (v.deleted_at IS NOT NULL OR NOT v.is_active OR (v.valid_from IS NOT NULL AND v.valid_from > now()) OR (v.valid_until IS NOT NULL AND v.valid_until < now()) OR (v.max_uses IS NOT NULL AND v.current_uses >= v.max_uses)) THEN
   RAISE EXCEPTION 'voucher_rejected';
  END IF;
  UPDATE vouchers SET current_uses = current_uses + 1, updated_at = now() WHERE id = NEW.voucher_id;
  NEW.voucher_use_released_at := NULL;
  NEW.voucher_use_claimed_at := now();
 END IF;
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER transaction_voucher_accounting BEFORE UPDATE ON transactions FOR EACH ROW EXECUTE FUNCTION transaction_voucher_accounting();
