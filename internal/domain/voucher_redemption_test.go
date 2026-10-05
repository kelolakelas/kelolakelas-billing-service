package domain

import "testing"

func TestKEL162VoucherDiscountFloorAndCaps(t *testing.T) {
	max := int64(750)
	for _, tt := range []struct {
		name         string
		v            Voucher
		amount, want int64
	}{
		{"percent floor", Voucher{DiscountType: VoucherDiscountPercentage, DiscountValue: 10}, 10003, 1000},
		{"max cap", Voucher{DiscountType: VoucherDiscountPercentage, DiscountValue: 10, MaxDiscountAmount: &max}, 10003, 750},
		{"fixed floor", Voucher{DiscountType: VoucherDiscountFixedAmount, DiscountValue: 999.99}, 10003, 999},
		{"subtotal cap", Voucher{DiscountType: VoucherDiscountFixedAmount, DiscountValue: 20000}, 10003, 10003},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeVoucherDiscount(&tt.v, tt.amount); got != tt.want {
				t.Fatalf("got=%d want=%d", got, tt.want)
			}
		})
	}
}
