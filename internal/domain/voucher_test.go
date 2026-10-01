package domain

import (
	"testing"
	"time"
)

// KEL-161: shared create/update validation semantics.
func TestValidateVoucherFields(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Hour)
	maxDiscount := int64(5000)
	maxUses := 10

	valid := func() (string, string, float64, int64, *int64, *int, int, *time.Time, *time.Time) {
		return "HEMAT10", VoucherDiscountPercentage, 10, 0, &maxDiscount, &maxUses, 0, &now, &later
	}

	if err := ValidateVoucherFields(valid()); err != nil {
		t.Fatalf("valid percentage voucher rejected: %v", err)
	}

	code, typ, value, min, maxD, uses, current, from, until := valid()
	_ = code
	_ = typ
	_ = value
	_ = min
	_ = maxD
	_ = uses
	_ = current
	_ = from
	_ = until

	for name, mutate := range map[string]func() error{
		"blank code": func() error {
			c, ty, v, mn, md, u, cu, f, un := valid()
			c = "   "
			return ValidateVoucherFields(c, ty, v, mn, md, u, cu, f, un)
		},
		"percentage zero": func() error {
			c, ty, _, mn, md, u, cu, f, un := valid()
			return ValidateVoucherFields(c, ty, 0, mn, md, u, cu, f, un)
		},
		"percentage over 100": func() error {
			c, ty, _, mn, md, u, cu, f, un := valid()
			return ValidateVoucherFields(c, ty, 101, mn, md, u, cu, f, un)
		},
		"fixed nominal zero": func() error {
			c, _, _, mn, md, u, cu, f, un := valid()
			return ValidateVoucherFields(c, VoucherDiscountFixedAmount, 0, mn, md, u, cu, f, un)
		},
		"unknown discount type": func() error {
			c, _, _, mn, md, u, cu, f, un := valid()
			return ValidateVoucherFields(c, "bogus", 10, mn, md, u, cu, f, un)
		},
		"negative min amount": func() error {
			c, ty, v, _, md, u, cu, f, un := valid()
			return ValidateVoucherFields(c, ty, v, -1, md, u, cu, f, un)
		},
		"non-positive max discount": func() error {
			c, ty, v, mn, _, u, cu, f, un := valid()
			zero := int64(0)
			return ValidateVoucherFields(c, ty, v, mn, &zero, u, cu, f, un)
		},
		"inverted date range": func() error {
			c, ty, v, mn, md, u, cu, _, _ := valid()
			return ValidateVoucherFields(c, ty, v, mn, md, u, cu, &later, &now)
		},
		"max uses below current uses": func() error {
			c, ty, v, mn, md, _, _, f, un := valid()
			cap := 2
			return ValidateVoucherFields(c, ty, v, mn, md, &cap, 3, f, un)
		},
		"non-positive max uses": func() error {
			c, ty, v, mn, md, _, cu, f, un := valid()
			zero := 0
			return ValidateVoucherFields(c, ty, v, mn, md, &zero, cu, f, un)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); err != ErrVoucherInvalid {
				t.Fatalf("err=%v, want ErrVoucherInvalid", err)
			}
		})
	}

	// Boundary values the contract names explicitly.
	c, ty, _, mn, md, u, _, f, un := valid()
	if err := ValidateVoucherFields(c, ty, 1, mn, md, u, 0, f, un); err != nil {
		t.Fatalf("percentage 1 rejected: %v", err)
	}
	if err := ValidateVoucherFields(c, ty, 100, mn, md, u, 0, f, un); err != nil {
		t.Fatalf("percentage 100 rejected: %v", err)
	}
	c2, _, _, mn2, md2, u2, _, f2, un2 := valid()
	if err := ValidateVoucherFields(c2, VoucherDiscountFixedAmount, 1, mn2, md2, u2, 0, f2, un2); err != nil {
		t.Fatalf("fixed nominal 1 rejected: %v", err)
	}
	// Reactivating an expired voucher is allowed: validity window never blocks.
	if err := ValidateVoucherFields(c, ty, 10, mn, md, u, 0, &later, &now); err == nil {
		t.Fatalf("inverted range unexpectedly accepted outside update path")
	}
}

func TestNormalizeVoucherCode(t *testing.T) {
	if got := NormalizeVoucherCode("  hemat10 "); got != "HEMAT10" {
		t.Fatalf("got %q, want HEMAT10", got)
	}
	if got := NormalizeVoucherCode("HEMAT10"); got != "HEMAT10" {
		t.Fatalf("got %q, want HEMAT10", got)
	}
}
