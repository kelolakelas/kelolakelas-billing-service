package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// fakeEmailSender records every message the worker asks the provider to send, so a
// test can prove the renewal payment-link email is addressed to the parent's
// billing email (KEL-75) without a live provider.
type fakeEmailSender struct {
	messages []domain.EmailMessage
	err      error
}

func (f *fakeEmailSender) Send(_ context.Context, message domain.EmailMessage) error {
	if f.err != nil {
		return f.err
	}
	f.messages = append(f.messages, message)
	return nil
}

// Acceptance criterion: a checkout that carries the parent's email claim stores it
// as the transaction's billing_email, which is the address the Duitku invoice is
// issued to.
func TestGenerateSubscriptionPaymentStoresSenderEmailAsBillingEmail(t *testing.T) {
	repo := &transactionRepoStub{missing: true}
	gateway := &invoiceGatewayStub{}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, claimTestConfig())

	email := "parent@example.com"
	_, err := usecase.GenerateSubscriptionPayment(context.Background(), &domain.GenerateSubscriptionPaymentRequest{
		EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(),
		BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000,
		SenderEmail: email,
	})
	if err != nil {
		t.Fatalf("GenerateSubscriptionPayment() error = %v", err)
	}
	if repo.transaction.BillingEmail != email {
		t.Fatalf("billing_email = %q, want %q", repo.transaction.BillingEmail, email)
	}
	if len(gateway.requests) != 1 || gateway.requests[0].Email != email {
		t.Fatalf("duitku email = %v, want %q", gateway.requests, email)
	}
}

// Rollout compatibility: a request without sender_email must keep creating the
// transaction exactly like before, with an empty billing email.
func TestGenerateSubscriptionPaymentWithoutSenderEmailStillSucceeds(t *testing.T) {
	repo := &transactionRepoStub{missing: true}
	gateway := &invoiceGatewayStub{}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, claimTestConfig())

	response, err := usecase.GenerateSubscriptionPayment(context.Background(), &domain.GenerateSubscriptionPaymentRequest{
		EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(),
		BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000,
	})
	if err != nil {
		t.Fatalf("GenerateSubscriptionPayment() error = %v", err)
	}
	if response.CheckoutSessionURL == "" {
		t.Fatal("checkout session URL was not returned")
	}
	if repo.createCount != 1 {
		t.Fatalf("transaction create count = %d, want 1", repo.createCount)
	}
	if repo.transaction.BillingEmail != "" {
		t.Fatalf("billing_email = %q, want empty", repo.transaction.BillingEmail)
	}
}

// Acceptance criterion: the subscription worker sends the renewal payment-link
// email for a new subscription whose transaction carries a billing email,
// addressed to that email.
func TestSubscriptionWorkerSendsRenewalPaymentLinkToBillingEmail(t *testing.T) {
	tx := claimTestTransaction(uuid.New())
	link := "https://pay.example.com/renewal"
	tx.CheckoutSessionURL = &link
	tx.BillingEmail = "parent@example.com"
	sub := subscriptionForRenewal(tx, uuid.New())
	sub.ParentName = "Budi"
	sub.ClassName = "Math"

	repo := &emailClaimRepoStub{transactionRepoStub: &transactionRepoStub{transaction: tx, periodTx: tx}, persisted: tx}
	email := &fakeEmailSender{}
	worker := NewSubscriptionWorker(&renewalSubscriptionRepoStub{subscription: sub}, repo, &invoiceGatewayStub{}, email, claimTestConfig(), renewalClock{now: time.Now()})

	worker.RunOnce(context.Background())

	if len(email.messages) != 1 {
		t.Fatalf("emails sent = %d, want 1 (%+v)", len(email.messages), email.messages)
	}
	if email.messages[0].To != "parent@example.com" {
		t.Fatalf("email to = %q, want parent@example.com", email.messages[0].To)
	}
	if email.messages[0].Subject != "Payment link subscription" {
		t.Fatalf("subject = %q, want the payment link email", email.messages[0].Subject)
	}
	if tx.PaymentLinkSentAt == nil {
		t.Fatal("payment_link_sent_at was not recorded after the email was sent")
	}
}

// Regression for the current behaviour: without a billing email there is no
// destination and the worker must not send anything.
func TestSubscriptionWorkerSkipsRenewalEmailWithoutBillingEmail(t *testing.T) {
	tx := claimTestTransaction(uuid.New())
	link := "https://pay.example.com/renewal"
	tx.CheckoutSessionURL = &link
	sub := subscriptionForRenewal(tx, uuid.New())

	repo := &emailClaimRepoStub{transactionRepoStub: &transactionRepoStub{transaction: tx, periodTx: tx}, persisted: tx}
	email := &fakeEmailSender{}
	worker := NewSubscriptionWorker(&renewalSubscriptionRepoStub{subscription: sub}, repo, &invoiceGatewayStub{}, email, claimTestConfig(), renewalClock{now: time.Now()})

	worker.RunOnce(context.Background())

	if len(email.messages) != 0 {
		t.Fatalf("emails sent = %d, want 0", len(email.messages))
	}
	if tx.PaymentLinkSentAt != nil {
		t.Fatal("payment_link_sent_at must stay empty when there is no billing email")
	}
}
