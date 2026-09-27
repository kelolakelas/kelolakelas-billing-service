package domain

import (
	"context"
	"errors"
	"math"
)

// Platform fee policy bounds (KEL-99). They mirror identity's validation of
// PLATFORM_FEE_POLICY; billing re-checks them because it must never charge a
// rule it cannot trust.
const (
	PlatformFeeMaxPercentBps int64 = 2000
	PlatformFeeMaxFixedFee   int64 = 50000
)

// PlatformFeeExceedsGrossCode is the stable machine-readable code returned with
// HTTP 422 when platform_fee + payment_gateway_fee would exceed gross_amount.
const (
	PlatformFeeExceedsGrossCode    = "platform_fee_exceeds_gross"
	PlatformFeeExceedsGrossMessage = "Biaya platform melebihi jumlah pembayaran"
)

var (
	// ErrPlatformFeeExceedsGross rejects a new transaction whose platform fee plus
	// payment gateway fee is larger than its gross amount. Nothing is written.
	ErrPlatformFeeExceedsGross = errors.New("platform fee exceeds gross amount")
	// ErrPlatformFeePolicyUnavailable means the applied platform fee policy could
	// not be read or was malformed. Billing refuses the invoice instead of
	// falling back to a 0% fee.
	ErrPlatformFeePolicyUnavailable = errors.New("platform fee policy unavailable")
	// ErrPlatformFeeAmountOutOfRange rejects a gross amount so large that the fee
	// computation would overflow.
	ErrPlatformFeeAmountOutOfRange = errors.New("gross amount too large for platform fee computation")
)

// PlatformFeePolicy is the applied platform fee rule billing received from
// identity, with the configuration version it came from. Version 0 is the
// explicit applied baseline of 0 bps + Rp0.
type PlatformFeePolicy struct {
	Version    int64
	PercentBps int64
	FixedFee   int64
}

// Validate reports whether the rule is within the owner-approved bounds.
func (p PlatformFeePolicy) Validate() error {
	if p.Version < 0 || p.PercentBps < 0 || p.PercentBps > PlatformFeeMaxPercentBps || p.FixedFee < 0 || p.FixedFee > PlatformFeeMaxFixedFee {
		return ErrPlatformFeePolicyUnavailable
	}
	return nil
}

// Fee returns floor(gross * percent_bps / 10000) + fixed_fee. gross must be
// positive; the computation is rejected instead of overflowing.
func (p PlatformFeePolicy) Fee(gross int64) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	if gross <= 0 {
		return 0, ErrPlatformFeeAmountOutOfRange
	}
	if p.PercentBps > 0 && gross > (math.MaxInt64-p.FixedFee)/p.PercentBps {
		return 0, ErrPlatformFeeAmountOutOfRange
	}
	return gross*p.PercentBps/10000 + p.FixedFee, nil
}

// PlatformFeeBreakdown is the fee snapshot a new transaction stores.
type PlatformFeeBreakdown struct {
	Policy      PlatformFeePolicy
	PlatformFee int64
	NetAmount   int64
}

// ComputePlatformFee applies policy to a new transaction's gross amount and
// gateway fee. The tenant bears both fees: net = gross - platform - gateway.
// A sum above gross is ErrPlatformFeeExceedsGross.
func ComputePlatformFee(policy PlatformFeePolicy, gross, gatewayFee int64) (PlatformFeeBreakdown, error) {
	if gatewayFee < 0 {
		return PlatformFeeBreakdown{}, ErrPlatformFeeAmountOutOfRange
	}
	fee, err := policy.Fee(gross)
	if err != nil {
		return PlatformFeeBreakdown{}, err
	}
	if fee > gross || gatewayFee > gross-fee {
		return PlatformFeeBreakdown{}, ErrPlatformFeeExceedsGross
	}
	return PlatformFeeBreakdown{Policy: policy, PlatformFee: fee, NetAmount: gross - fee - gatewayFee}, nil
}

// Apply stores the snapshot on a transaction that has not been persisted yet.
func (b PlatformFeeBreakdown) Apply(tx *Transaction) {
	version, percent, fixed := b.Policy.Version, b.Policy.PercentBps, b.Policy.FixedFee
	tx.PlatformFee = b.PlatformFee
	tx.NetAmount = b.NetAmount
	tx.PlatformFeePolicyVersion = &version
	tx.PlatformFeePercentBps = &percent
	tx.PlatformFeeFixed = &fixed
}

// PlatformFeePolicyReader returns the applied platform fee policy. Every error,
// including a policy that is not applied yet or malformed, must stop invoice
// creation.
type PlatformFeePolicyReader interface {
	AppliedPlatformFeePolicy(ctx context.Context) (PlatformFeePolicy, error)
}
