package domain

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"time"
)

// VoucherRejectedCode is the stable machine-readable code returned with HTTP
// 422 when checkout cannot use a voucher (KEL-162). One code for every reason
// so a caller cannot probe which codes exist for which tenant, mirroring
// ErrVoucherNotFound's tenant-probing protection.
const (
	VoucherRejectedCode    = "voucher_rejected"
	VoucherRejectedMessage = "Voucher tidak dapat digunakan. Silakan checkout ulang tanpa voucher."
)

var (
	// ErrVoucherRejected refuses a checkout voucher. It covers unknown code,
	// inactive, outside its validity window, below the minimum transaction,
	// exhausted quota (max_uses), and a replacement invoice that cannot
	// re-reserve its voucher. Nothing is written.
	ErrVoucherRejected = errors.New("voucher cannot be used")
)

// VoucherRedeemValidation is the rule set a checkout voucher must pass before
// its discount is reserved (KEL-162). The rules mirror ValidateVoucherFields,
// but redemption reads the stored row, not a write payload.
type VoucherRedeemValidation struct {
	Now    time.Time
	Code   string
	Amount int64 // subtotal before the discount
}

// ValidateVoucherForRedemption checks a stored voucher against a checkout
// attempt. It mutates nothing: the caller reserves usage separately and
// atomically. An invalid request (blank/oversized code, non-positive amount)
// is ErrVoucherRejected like any other refusal, because the voucher path must
// fail closed with one stable error.
func ValidateVoucherForRedemption(voucher *Voucher, checkout VoucherRedeemValidation) error {
	if voucher == nil {
		return ErrVoucherRejected
	}
	code := NormalizeVoucherCode(checkout.Code)
	if code == "" || len([]rune(code)) > 255 || checkout.Amount <= 0 {
		return ErrVoucherRejected
	}
	if err := ValidateVoucherFields(voucher.Code, voucher.DiscountType, voucher.DiscountValue, voucher.MinTransactionAmount, voucher.MaxDiscountAmount, nil, 0, voucher.ValidFrom, voucher.ValidUntil); err != nil {
		return ErrVoucherRejected
	}
	if !voucher.IsActive {
		return ErrVoucherRejected
	}
	if voucher.ValidFrom != nil && checkout.Now.Before(*voucher.ValidFrom) {
		return ErrVoucherRejected
	}
	if voucher.ValidUntil != nil && checkout.Now.After(*voucher.ValidUntil) {
		return ErrVoucherRejected
	}
	if checkout.Amount < voucher.MinTransactionAmount {
		return ErrVoucherRejected
	}
	return nil
}

// VoucherRedeemQuotaLeft reports how many further reservations a voucher can
// still grant for an active checkout (KEL-162). A nil MaxUses is unlimited.
// The answer can legitimately be negative: current_uses may exceed max_uses
// after a late confirmed payment re-took its released reservation over cap
// (owner decision 2). Quota for NEW reservations is then simply exhausted.
func VoucherRedeemQuotaLeft(voucher *Voucher) int {
	if voucher == nil || voucher.MaxUses == nil {
		return math.MaxInt
	}
	return *voucher.MaxUses - voucher.CurrentUses
}

// ComputeVoucherDiscount returns the discount a voucher grants on a subtotal,
// capped by max_discount_amount and by the subtotal itself so the resulting
// gross is always positive. DiscountValue is 1-100 for `percentage` (floor of
// the prorated amount, mirroring the platform fee's floor semantics) and a
// positive IDR nominal for `fixed_amount`. An unknown type discounts nothing:
// stored rows are validated on write, so an unknown type is a legacy anomaly
// that must not block checkout with an over-applied discount.
func ComputeVoucherDiscount(voucher *Voucher, subtotal int64) int64 {
	if voucher == nil || subtotal <= 0 {
		return 0
	}
	var discount int64
	switch voucher.DiscountType {
	case VoucherDiscountPercentage:
		if voucher.DiscountValue < 1 || voucher.DiscountValue > 100 {
			return 0
		}
		// Stored numeric(15,2) percentages are exact decimal rationals; avoid
		// float64 rounding up the floor for large subtotals.
		value, ok := new(big.Rat).SetString(strconv.FormatFloat(voucher.DiscountValue, 'f', 2, 64))
		if !ok {
			return 0
		}
		value.Mul(value, new(big.Rat).SetInt64(subtotal))
		value.Quo(value, new(big.Rat).SetInt64(100))
		discount = new(big.Int).Quo(value.Num(), value.Denom()).Int64()
	case VoucherDiscountFixedAmount:
		if voucher.DiscountValue <= 0 {
			return 0
		}
		discount = int64(voucher.DiscountValue)
	default:
		return 0
	}
	if voucher.MaxDiscountAmount != nil && discount > *voucher.MaxDiscountAmount {
		discount = *voucher.MaxDiscountAmount
	}
	if discount > subtotal {
		discount = subtotal
	}
	return discount
}

// VoucherRedemption is the decision a checkout receives: which voucher row was
// locked, and the discount to subtract from the subtotal. The caller persists
// the voucher id and discount as the transaction snapshot.
type VoucherRedemption struct {
	Voucher        *Voucher
	DiscountAmount int64
}
