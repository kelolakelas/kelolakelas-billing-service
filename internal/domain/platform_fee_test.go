package domain

import (
	"errors"
	"math"
	"testing"
)

func TestComputePlatformFeeAcceptanceExamples(t *testing.T) {
	cases := []struct {
		name        string
		policy      PlatformFeePolicy
		gross, gw   int64
		wantFee     int64
		wantNet     int64
		wantVersion int64
	}{
		{"5% + Rp1000 on Rp180000", PlatformFeePolicy{Version: 3, PercentBps: 500, FixedFee: 1000}, 180000, 0, 10000, 170000, 3},
		{"2.5% on Rp99999 is floored", PlatformFeePolicy{Version: 4, PercentBps: 250}, 99999, 0, 2499, 97500, 4},
		{"tenant bears both fees", PlatformFeePolicy{Version: 1, PercentBps: 500, FixedFee: 1000}, 180000, 4000, 10000, 166000, 1},
		{"baseline 0/0 keeps gross", PlatformFeePolicy{}, 40000, 2000, 0, 38000, 0},
		{"fee exactly equal to gross is allowed", PlatformFeePolicy{Version: 2, FixedFee: 2000}, 3000, 1000, 2000, 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputePlatformFee(tc.policy, tc.gross, tc.gw)
			if err != nil {
				t.Fatalf("ComputePlatformFee() error = %v", err)
			}
			if got.PlatformFee != tc.wantFee || got.NetAmount != tc.wantNet || got.Policy.Version != tc.wantVersion {
				t.Fatalf("got fee=%d net=%d version=%d, want fee=%d net=%d version=%d", got.PlatformFee, got.NetAmount, got.Policy.Version, tc.wantFee, tc.wantNet, tc.wantVersion)
			}
		})
	}
}

func TestComputePlatformFeeRejectsFeesAboveGross(t *testing.T) {
	// gross Rp2000, fixed fee Rp2500 and a gateway fee Rp1000.
	_, err := ComputePlatformFee(PlatformFeePolicy{Version: 5, FixedFee: 2500}, 2000, 1000)
	if !errors.Is(err, ErrPlatformFeeExceedsGross) {
		t.Fatalf("error = %v, want ErrPlatformFeeExceedsGross", err)
	}
	// The platform fee alone fits but platform + gateway does not.
	_, err = ComputePlatformFee(PlatformFeePolicy{Version: 5, FixedFee: 1500}, 2000, 501)
	if !errors.Is(err, ErrPlatformFeeExceedsGross) {
		t.Fatalf("error = %v, want ErrPlatformFeeExceedsGross", err)
	}
}

func TestPlatformFeePolicyRejectsOutOfBoundsAndOverflow(t *testing.T) {
	for _, policy := range []PlatformFeePolicy{
		{Version: -1},
		{PercentBps: -1},
		{PercentBps: 2001},
		{FixedFee: -1},
		{FixedFee: 50001},
	} {
		if _, err := ComputePlatformFee(policy, 10000, 0); !errors.Is(err, ErrPlatformFeePolicyUnavailable) {
			t.Fatalf("policy %+v error = %v, want ErrPlatformFeePolicyUnavailable", policy, err)
		}
	}
	if _, err := ComputePlatformFee(PlatformFeePolicy{PercentBps: 2000}, math.MaxInt64/100, 0); !errors.Is(err, ErrPlatformFeeAmountOutOfRange) {
		t.Fatalf("overflow error = %v, want ErrPlatformFeeAmountOutOfRange", err)
	}
	if _, err := ComputePlatformFee(PlatformFeePolicy{}, 0, 0); !errors.Is(err, ErrPlatformFeeAmountOutOfRange) {
		t.Fatalf("zero gross error = %v, want ErrPlatformFeeAmountOutOfRange", err)
	}
}

func TestPlatformFeeBreakdownApplyWritesTheWholeSnapshot(t *testing.T) {
	breakdown, err := ComputePlatformFee(PlatformFeePolicy{Version: 7, PercentBps: 500, FixedFee: 1000}, 180000, 0)
	if err != nil {
		t.Fatal(err)
	}
	var tx Transaction
	breakdown.Apply(&tx)
	if tx.PlatformFee != 10000 || tx.NetAmount != 170000 {
		t.Fatalf("amounts = %d/%d", tx.PlatformFee, tx.NetAmount)
	}
	if tx.PlatformFeePolicyVersion == nil || *tx.PlatformFeePolicyVersion != 7 || tx.PlatformFeePercentBps == nil || *tx.PlatformFeePercentBps != 500 || tx.PlatformFeeFixed == nil || *tx.PlatformFeeFixed != 1000 {
		t.Fatalf("snapshot = %v/%v/%v, want 7/500/1000", tx.PlatformFeePolicyVersion, tx.PlatformFeePercentBps, tx.PlatformFeeFixed)
	}
}
