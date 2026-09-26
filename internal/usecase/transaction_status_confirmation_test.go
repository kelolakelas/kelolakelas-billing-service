package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func TestWebhookStatusConfirmationGuardsWalletAndLedger(t *testing.T) {
	for _, tc := range []struct {
		name      string
		code      string
		amount    int64
		reference string
		order     string
		err       error
		paid      bool
	}{
		{name: "confirmed", code: "00", amount: 40000, reference: "REF-PAID", paid: true},
		{name: "pending", code: "01", amount: 40000, reference: "REF-PAID"},
		{name: "cancelled", code: "02", amount: 40000, reference: "REF-PAID"},
		{name: "amount mismatch", code: "00", amount: 39999, reference: "REF-PAID"},
		{name: "reference mismatch", code: "00", amount: 40000, reference: "OTHER"},
		{name: "order mismatch", code: "00", amount: 40000, reference: "REF-PAID", order: "OTHER"},
		{name: "timeout", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &domain.Transaction{ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), EnrollmentID: uuid.New(), GrossAmount: 40000, NetAmount: 38000, Status: domain.TransactionStatusPending}
			repo := &transactionRepoStub{transaction: tx}
			wallet := &walletRepoStub{}
			ledger := &ledgerRepoStub{}
			status := &domain.PaymentStatus{MerchantOrderID: tx.MerchantOrderID, Reference: tc.reference, Amount: tc.amount, StatusCode: tc.code}
			if tc.order != "" {
				status.MerchantOrderID = tc.order
			}
			gateway := &invoiceGatewayStub{status: status, statusErr: tc.err}
			u := NewTransactionUsecase(repo, wallet, ledger, &subscriptionRepoStub{}, gateway, nil, expiryTestConfig())
			callback := callbackPayload(tx, domain.ResultCodeSuccess)
			err := u.HandleDuitkuWebhook(context.Background(), callback)
			if tc.paid {
				if err != nil {
					t.Fatal(err)
				}
				if tx.Status != domain.TransactionStatusPaid || wallet.balance != 38000 || ledger.entries != 1 || repo.updateCount != 1 {
					t.Fatalf("settlement: tx=%s wallet=%d ledger=%d updates=%d", tx.Status, wallet.balance, ledger.entries, repo.updateCount)
				}
				if err := u.HandleDuitkuWebhook(context.Background(), callback); err != nil {
					t.Fatal(err)
				}
				if wallet.balance != 38000 || ledger.entries != 1 || gateway.statusCalls != 1 {
					t.Fatalf("replay: wallet=%d ledger=%d status calls=%d", wallet.balance, ledger.entries, gateway.statusCalls)
				}
			} else {
				if err == nil {
					t.Fatal("unconfirmed callback accepted")
				}
				if tc.err != nil && !errors.Is(err, tc.err) {
					t.Fatalf("error = %v", err)
				}
				if tx.Status != domain.TransactionStatusPending || wallet.balance != 0 || ledger.entries != 0 || repo.updateCount != 0 {
					t.Fatalf("unconfirmed mutation: tx=%s wallet=%d ledger=%d updates=%d", tx.Status, wallet.balance, ledger.entries, repo.updateCount)
				}
				if gateway.statusCalls != 1 {
					t.Fatalf("status calls = %d", gateway.statusCalls)
				}
			}
		})
	}
}
