package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func TestSubscriptionInvoicePaymentMethodAndReplay(t *testing.T) {
	for _, method := range []string{"", "VC", "VA", "BC", "SP", "NQ"} {
		t.Run(method, func(t *testing.T) {
			repo := &transactionRepoStub{missing: true}
			gateway := &invoiceGatewayStub{}
			uc := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, expiryTestConfig())
			req := &domain.GenerateSubscriptionPaymentRequest{EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(), BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000, PaymentMethod: method}
			first, err := uc.GenerateSubscriptionPayment(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			want := method
			if want == "" {
				want = "VC"
			}
			if len(gateway.requests) != 1 || gateway.requests[0].PaymentMethod != want || repo.transaction.PaymentMethod == nil || *repo.transaction.PaymentMethod != want {
				t.Fatalf("gateway=%v persisted method=%v, want %s", gateway.requests, repo.transaction.PaymentMethod, want)
			}
			req.PaymentMethod = "NQ"
			second, err := uc.GenerateSubscriptionPayment(context.Background(), req)
			if err != nil || second.CheckoutSessionURL != first.CheckoutSessionURL || len(gateway.requests) != 1 || repo.createCount != 1 || *repo.transaction.PaymentMethod != want {
				t.Fatalf("replay=%+v err=%v gateway=%d creates=%d method=%v", second, err, len(gateway.requests), repo.createCount, repo.transaction.PaymentMethod)
			}
		})
	}
}

func TestSubscriptionInvoiceRejectsUnknownMethodBeforeWrite(t *testing.T) {
	repo := &transactionRepoStub{missing: true}
	gateway := &invoiceGatewayStub{}
	uc := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, expiryTestConfig())
	_, err := uc.GenerateSubscriptionPayment(context.Background(), &domain.GenerateSubscriptionPaymentRequest{EnrollmentID: uuid.New(), PaymentMethod: "OV"})
	if !errors.Is(err, domain.ErrInvalidPaymentMethod) || repo.createCount != 0 || len(gateway.requests) != 0 {
		t.Fatalf("error=%v creates=%d gateway=%d", err, repo.createCount, len(gateway.requests))
	}
}

func TestExpiredInvoiceRetainsOriginalPaymentMethod(t *testing.T) {
	for _, original := range []string{"", "SP"} {
		t.Run(original, func(t *testing.T) {
			tx := claimTestTransaction(uuid.New())
			tx.Status = domain.TransactionStatusExpired
			if original != "" {
				tx.PaymentMethod = &original
			}
			repo := &transactionRepoStub{transaction: tx}
			gateway := &invoiceGatewayStub{}
			uc := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, expiryTestConfig())
			req := claimTestRequest(tx)
			req.PaymentMethod = "VA"
			if _, err := uc.GenerateSubscriptionPayment(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			want := original
			if want == "" {
				want = "VC"
			}
			if len(gateway.requests) != 1 || gateway.requests[0].PaymentMethod != want || repo.createCount != 0 {
				t.Fatalf("reissued method=%v creates=%d want=%s", gateway.requests, repo.createCount, want)
			}
		})
	}
}

func TestInvoiceCallbackKeepsSelectedChannelAndFeeSnapshot(t *testing.T) {
	repo := &transactionRepoStub{missing: true}
	gateway := &invoiceGatewayStub{}
	reconciliations := &reconciliationRepoStub{item: &domain.PaymentReconciliation{ID: uuid.New(), AttemptCount: 1}}
	uc := newTransactionUsecaseForTest(repo, reconciliations, gateway, &academicActivationStub{}, expiryTestConfig())
	req := &domain.GenerateSubscriptionPaymentRequest{EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(), BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000, PaymentMethod: "BC"}
	if _, err := uc.GenerateSubscriptionPayment(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	tx := repo.transaction
	fee := tx.PlatformFee
	gateway.status = &domain.PaymentStatus{MerchantOrderID: tx.MerchantOrderID, Reference: *tx.PaymentIntentID, Amount: tx.GrossAmount, StatusCode: domain.ResultCodeSuccess}
	payload := callbackPayload(tx, domain.ResultCodeSuccess)
	payload.Reference = *tx.PaymentIntentID
	payload.PaymentCode = "BC"
	if err := uc.HandleDuitkuWebhook(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if err := uc.HandleDuitkuWebhook(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if tx.Status != domain.TransactionStatusPaid || tx.PaymentMethod == nil || *tx.PaymentMethod != "BC" || tx.PlatformFee != fee {
		t.Fatalf("status=%s channel=%v fee=%d want fee=%d", tx.Status, tx.PaymentMethod, tx.PlatformFee, fee)
	}
}
