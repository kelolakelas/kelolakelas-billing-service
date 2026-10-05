package usecase

import (
	"context"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"testing"
	"time"
)

func TestVoucherCheckoutDiscountDoesNotCarryToRenewal(t *testing.T) {
	base := claimTestTransaction(uuid.New())
	voucherID := uuid.New()
	base.VoucherID = &voucherID
	base.SubtotalAmount, base.DiscountAmount, base.GrossAmount = 200000, 20000, 180000
	sub := subscriptionForRenewal(base, uuid.New())
	repo := &transactionRepoStub{transaction: base}
	reader := &feePolicyReaderStub{policy: domain.PlatformFeePolicy{Version: 4, PercentBps: 250}}
	worker := NewSubscriptionWorker(&renewalSubscriptionRepoStub{subscription: sub}, repo, &invoiceGatewayStub{}, nil, claimTestConfig(), renewalClock{now: time.Now()}).WithPlatformFeePolicy(reader)
	worker.RunOnce(context.Background())
	if repo.createCount != 1 || repo.transaction == base {
		t.Fatal("renewal not created")
	}
	tx := repo.transaction
	if tx.VoucherID != nil || tx.DiscountAmount != 0 || tx.GrossAmount != 200000 || tx.PlatformFee != 5000 {
		t.Fatalf("renewal inherited voucher: %+v", tx)
	}
	if base.DiscountAmount != 20000 {
		t.Fatal("checkout snapshot changed")
	}
}
