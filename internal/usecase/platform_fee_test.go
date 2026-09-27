package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// feePolicyReaderStub returns a fixed applied policy (or error) and counts
// reads. setPolicy models an operator applying a new version mid-flight.
type feePolicyReaderStub struct {
	mu     sync.Mutex
	policy domain.PlatformFeePolicy
	err    error
	reads  int
}

func (s *feePolicyReaderStub) AppliedPlatformFeePolicy(context.Context) (domain.PlatformFeePolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return s.policy, s.err
}

func (s *feePolicyReaderStub) setPolicy(policy domain.PlatformFeePolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = policy
}

func newFeeTestUsecase(repo *transactionRepoStub, gateway domain.PaymentGateway, reader domain.PlatformFeePolicyReader) TransactionUsecase {
	return WithPlatformFeePolicy(NewTransactionUsecaseWithReconciliation(
		repo, &walletRepoStub{}, &ledgerRepoStub{}, &subscriptionRepoStub{}, gateway, &academicActivationStub{}, claimTestConfig(), nil, &reconciliationRepoStub{},
	), reader)
}

func feeTestRequest(subtotal, gatewayFee, requestPlatformFee int64) *domain.GenerateSubscriptionPaymentRequest {
	return &domain.GenerateSubscriptionPaymentRequest{
		EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(),
		BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: subtotal,
		PaymentGatewayFee: gatewayFee, PlatformFee: requestPlatformFee,
	}
}

func assertSnapshot(t *testing.T, tx *domain.Transaction, version, percent, fixed, fee, net int64) {
	t.Helper()
	if tx.PlatformFee != fee || tx.NetAmount != net {
		t.Fatalf("platform_fee/net_amount = %d/%d, want %d/%d", tx.PlatformFee, tx.NetAmount, fee, net)
	}
	if tx.PlatformFeePolicyVersion == nil || *tx.PlatformFeePolicyVersion != version ||
		tx.PlatformFeePercentBps == nil || *tx.PlatformFeePercentBps != percent ||
		tx.PlatformFeeFixed == nil || *tx.PlatformFeeFixed != fixed {
		t.Fatalf("snapshot = %v/%v/%v, want %d/%d/%d", tx.PlatformFeePolicyVersion, tx.PlatformFeePercentBps, tx.PlatformFeeFixed, version, percent, fixed)
	}
}

// Acceptance example: 5% + Rp1000 on Rp180000 is Rp10000, and the caller's
// platform_fee is ignored as a source of truth.
func TestGenerateSubscriptionPaymentSnapshotsTheAppliedPlatformFee(t *testing.T) {
	repo := &transactionRepoStub{missing: true}
	reader := &feePolicyReaderStub{policy: domain.PlatformFeePolicy{Version: 3, PercentBps: 500, FixedFee: 1000}}
	uc := newFeeTestUsecase(repo, &invoiceGatewayStub{}, reader)

	if _, err := uc.GenerateSubscriptionPayment(context.Background(), feeTestRequest(180000, 0, 99999)); err != nil {
		t.Fatalf("GenerateSubscriptionPayment() error = %v", err)
	}
	if repo.createCount != 1 {
		t.Fatalf("transactions created = %d, want 1", repo.createCount)
	}
	assertSnapshot(t, repo.transaction, 3, 500, 1000, 10000, 170000)
}

func TestGenerateSubscriptionPaymentFloorsThePercentFee(t *testing.T) {
	repo := &transactionRepoStub{missing: true}
	uc := newFeeTestUsecase(repo, &invoiceGatewayStub{}, &feePolicyReaderStub{policy: domain.PlatformFeePolicy{Version: 4, PercentBps: 250}})

	if _, err := uc.GenerateSubscriptionPayment(context.Background(), feeTestRequest(99999, 0, 0)); err != nil {
		t.Fatalf("GenerateSubscriptionPayment() error = %v", err)
	}
	assertSnapshot(t, repo.transaction, 4, 250, 0, 2499, 97500)
}

// gross Rp2000 with fixed Rp2500 and gateway Rp1000 is rejected, and nothing
// is written or invoiced.
func TestGenerateSubscriptionPaymentRejectsFeesAboveGrossWithoutWriting(t *testing.T) {
	repo := &transactionRepoStub{missing: true}
	gateway := &invoiceGatewayStub{}
	uc := newFeeTestUsecase(repo, gateway, &feePolicyReaderStub{policy: domain.PlatformFeePolicy{Version: 5, FixedFee: 2500}})

	_, err := uc.GenerateSubscriptionPayment(context.Background(), feeTestRequest(2000, 1000, 0))
	if !errors.Is(err, domain.ErrPlatformFeeExceedsGross) {
		t.Fatalf("error = %v, want ErrPlatformFeeExceedsGross", err)
	}
	if repo.createCount != 0 || repo.updateCount != 0 || len(gateway.requests) != 0 {
		t.Fatalf("writes = create %d update %d invoices %d, want none", repo.createCount, repo.updateCount, len(gateway.requests))
	}
}

// No trustworthy applied policy means no transaction: never a silent 0% fee.
func TestGenerateSubscriptionPaymentFailsClosedWithoutAnAppliedPolicy(t *testing.T) {
	readers := map[string]domain.PlatformFeePolicyReader{
		"no reader":          nil,
		"identity down":      &feePolicyReaderStub{err: errors.New("rpc error: code = Unavailable")},
		"not applied":        &feePolicyReaderStub{err: domain.ErrPlatformFeePolicyUnavailable},
		"out of bounds rule": &feePolicyReaderStub{policy: domain.PlatformFeePolicy{Version: 9, PercentBps: 2001}},
	}
	for name, reader := range readers {
		t.Run(name, func(t *testing.T) {
			repo := &transactionRepoStub{missing: true}
			gateway := &invoiceGatewayStub{}
			uc := newFeeTestUsecase(repo, gateway, reader)

			_, err := uc.GenerateSubscriptionPayment(context.Background(), feeTestRequest(180000, 0, 0))
			if !errors.Is(err, domain.ErrPlatformFeePolicyUnavailable) {
				t.Fatalf("error = %v, want ErrPlatformFeePolicyUnavailable", err)
			}
			if repo.createCount != 0 || len(gateway.requests) != 0 {
				t.Fatalf("writes = create %d invoices %d, want none", repo.createCount, len(gateway.requests))
			}
		})
	}
}

// A version applied while a checkout is pending does not reprice it: the
// replacement invoice for the existing transaction keeps its snapshot, and the
// policy is not even read.
func TestGenerateSubscriptionPaymentReinvoiceKeepsTheOriginalSnapshot(t *testing.T) {
	reader := &feePolicyReaderStub{policy: domain.PlatformFeePolicy{Version: 3, PercentBps: 500, FixedFee: 1000}}
	repo := &transactionRepoStub{missing: true}
	uc := newFeeTestUsecase(repo, &invoiceGatewayStub{}, reader)
	req := feeTestRequest(180000, 0, 0)
	if _, err := uc.GenerateSubscriptionPayment(context.Background(), req); err != nil {
		t.Fatalf("first GenerateSubscriptionPayment() error = %v", err)
	}
	original := repo.transaction
	original.Status = domain.TransactionStatusFailed
	original.CheckoutSessionURL = nil
	readsBefore := reader.reads

	reader.setPolicy(domain.PlatformFeePolicy{Version: 4, PercentBps: 1000, FixedFee: 5000})
	if _, err := uc.GenerateSubscriptionPayment(context.Background(), req); err != nil {
		t.Fatalf("reinvoice GenerateSubscriptionPayment() error = %v", err)
	}
	if repo.createCount != 1 {
		t.Fatalf("transactions created = %d, want the original row reused", repo.createCount)
	}
	if reader.reads != readsBefore {
		t.Fatalf("policy reads = %d, want %d: an existing transaction is never repriced", reader.reads, readsBefore)
	}
	assertSnapshot(t, repo.transaction, 3, 500, 1000, 10000, 170000)
}

// Legacy transactions (no policy version) stay untouched when reinvoiced.
func TestGenerateSubscriptionPaymentReinvoiceLeavesLegacyTransactionUnpriced(t *testing.T) {
	legacy := claimTestTransaction(uuid.New())
	legacy.Status = domain.TransactionStatusFailed
	legacy.PlatformFee = 2000
	legacy.NetAmount = 38000
	repo := &transactionRepoStub{transaction: legacy}
	reader := &feePolicyReaderStub{policy: domain.PlatformFeePolicy{Version: 4, PercentBps: 1000}}
	uc := newFeeTestUsecase(repo, &invoiceGatewayStub{}, reader)

	if _, err := uc.GenerateSubscriptionPayment(context.Background(), claimTestRequest(legacy)); err != nil {
		t.Fatalf("GenerateSubscriptionPayment() error = %v", err)
	}
	if legacy.PlatformFeePolicyVersion != nil || legacy.PlatformFee != 2000 || legacy.NetAmount != 38000 {
		t.Fatalf("legacy transaction changed: version=%v fee=%d net=%d", legacy.PlatformFeePolicyVersion, legacy.PlatformFee, legacy.NetAmount)
	}
	if reader.reads != 0 {
		t.Fatalf("policy reads = %d, want 0", reader.reads)
	}
}

// A renewal is a new transaction priced by the policy applied when it is
// created, not by the base transaction's snapshot.
func TestSubscriptionWorkerPricesRenewalWithTheCurrentAppliedPolicy(t *testing.T) {
	base := claimTestTransaction(uuid.New())
	oldVersion, oldPercent, oldFixed := int64(3), int64(500), int64(1000)
	base.GrossAmount, base.PlatformFee, base.NetAmount = 180000, 10000, 170000
	base.PlatformFeePolicyVersion, base.PlatformFeePercentBps, base.PlatformFeeFixed = &oldVersion, &oldPercent, &oldFixed
	sub := subscriptionForRenewal(base, uuid.New())
	repo := &transactionRepoStub{transaction: base}
	reader := &feePolicyReaderStub{policy: domain.PlatformFeePolicy{Version: 4, PercentBps: 250}}
	worker := NewSubscriptionWorker(&renewalSubscriptionRepoStub{subscription: sub}, repo, &invoiceGatewayStub{}, nil, claimTestConfig(), renewalClock{now: time.Now()}).WithPlatformFeePolicy(reader)

	worker.RunOnce(context.Background())

	if repo.createCount != 1 || repo.transaction == base {
		t.Fatalf("renewal transactions created = %d, want 1 new row", repo.createCount)
	}
	assertSnapshot(t, repo.transaction, 4, 250, 0, 4500, 175500)
	if *base.PlatformFeePolicyVersion != 3 || base.PlatformFee != 10000 {
		t.Fatal("the base transaction's snapshot changed")
	}
}

func TestSubscriptionWorkerCreatesNoRenewalWithoutAnAppliedPolicy(t *testing.T) {
	base := claimTestTransaction(uuid.New())
	sub := subscriptionForRenewal(base, uuid.New())
	repo := &transactionRepoStub{transaction: base}
	gateway := &invoiceGatewayStub{}
	worker := NewSubscriptionWorker(&renewalSubscriptionRepoStub{subscription: sub}, repo, gateway, nil, claimTestConfig(), renewalClock{now: time.Now()}).WithPlatformFeePolicy(&feePolicyReaderStub{err: domain.ErrPlatformFeePolicyUnavailable})

	worker.RunOnce(context.Background())

	if repo.createCount != 0 || len(gateway.requests) != 0 {
		t.Fatalf("writes = create %d invoices %d, want none", repo.createCount, len(gateway.requests))
	}
}
