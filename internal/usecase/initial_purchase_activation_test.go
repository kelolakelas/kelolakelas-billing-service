package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// Generate the invoice through the real initial-purchase path: hand-built
// callback fixtures used to omit the subscription/period fields that it saves.
func TestGeneratedInitialPurchaseStillOwesActivation(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "group", true: "private"}[private], func(t *testing.T) {
			ctx := context.Background()
			repo := &transactionRepoStub{missing: true}
			recon := &reconciliationRepoStub{item: &domain.PaymentReconciliation{ID: uuid.New(), EnrollmentID: uuid.New()}}
			academic := &academicActivationStub{}
			gateway := &invoiceGatewayStub{}
			u := newTransactionUsecaseForTest(repo, recon, gateway, academic, claimTestConfig())
			req := &domain.GenerateSubscriptionPaymentRequest{EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000, PrivateScheduleRequest: private}
			if _, err := u.GenerateSubscriptionPayment(ctx, req); err != nil {
				t.Fatal(err)
			}
			tx := repo.transaction
			if tx.SubscriptionID == nil || tx.BillingPeriodStart == nil || tx.ID != req.EnrollmentID {
				t.Fatal("fixture no longer represents a generated initial purchase")
			}
			// The stub provider confirms the generated reference and amount.
			gateway.status = &domain.PaymentStatus{MerchantOrderID: tx.MerchantOrderID, Reference: *tx.PaymentIntentID, Amount: tx.GrossAmount, StatusCode: "00"}
			callback := callbackPayload(tx, "00")
			callback.Reference = *tx.PaymentIntentID
			for range 2 {
				if err := u.HandleDuitkuWebhook(ctx, callback); err != nil {
					t.Fatal(err)
				}
			}
			if tx.Status != "paid" || recon.ensureCount != 2 || academic.calls != 1 || !recon.active {
				t.Fatalf("paid=%s enqueues=%d activation calls=%d reconciled=%v", tx.Status, recon.ensureCount, academic.calls, recon.active)
			}
			if gateway.statusCalls != 1 || repo.createCount != 1 {
				t.Fatalf("replay duplicated effects: provider confirmations=%d transactions=%d", gateway.statusCalls, repo.createCount)
			}
		})
	}
}

func TestRenewalKeepsExistingEnrollmentOnPaidAndFailedCallback(t *testing.T) {
	for _, result := range []string{"00", "01"} {
		t.Run(result, func(t *testing.T) {
			sub, period := uuid.New(), time.Now()
			tx := claimTestTransaction(uuid.New())
			tx.SubscriptionID, tx.BillingPeriodStart = &sub, &period
			recon := &reconciliationRepoStub{}
			academic := &academicActivationStub{}
			u := newTransactionUsecaseForTest(&transactionRepoStub{transaction: tx}, recon, &invoiceGatewayStub{}, academic, claimTestConfig())
			if err := u.HandleDuitkuWebhook(context.Background(), callbackPayload(tx, result)); err != nil {
				t.Fatal(err)
			}
			if recon.ensureCount != 0 || recon.releaseCount != 0 || academic.calls != 0 {
				t.Fatal("renewal changed initial enrollment lifecycle")
			}
		})
	}
}

func TestInitialInvoiceFailureEnqueuesSeatReleaseWithPeriodMetadata(t *testing.T) {
	tx := claimTestTransaction(uuid.New())
	tx.ID = tx.EnrollmentID
	sub, period := uuid.New(), time.Now()
	tx.SubscriptionID, tx.BillingPeriodStart = &sub, &period
	recon := &reconciliationRepoStub{}
	u := newTransactionUsecaseForTest(&transactionRepoStub{transaction: tx}, recon, &invoiceGatewayStub{}, &academicActivationStub{}, claimTestConfig())
	if err := u.HandleDuitkuWebhook(context.Background(), callbackPayload(tx, "01")); err != nil {
		t.Fatal(err)
	}
	if recon.releaseCount != 1 || recon.ensureCount != 0 {
		t.Fatal("initial unpaid invoice did not release its hold")
	}
}
