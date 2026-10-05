package usecase

import (
	"context"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"testing"
)

func TestRefundedCallbackNeverReactivatesOrCredits(t *testing.T) {
	tx := cancelTestTransaction(domain.TransactionStatusRefunded)
	repo := &transactionRepoStub{transaction: tx}
	recon := &reconciliationRepoStub{}
	u := newTransactionUsecaseForTest(repo, recon, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig()).(*transactionUsecase)
	// No wallet or subscription dependency is provided: replay must return before
	// accessing either, even when a successful provider callback arrives late.
	err := u.handleDuitkuWebhookLocal(context.Background(), &domain.DuitkuCallbackPayload{MerchantOrderID: tx.ID.String(), ResultCode: domain.ResultCodeSuccess}, nil, nil)
	if err != nil || repo.updateCount != 0 || recon.ensureCount != 0 || tx.Status != "refunded" {
		t.Fatalf("err=%v writes=%d activations=%d status=%s", err, repo.updateCount, recon.ensureCount, tx.Status)
	}
}
