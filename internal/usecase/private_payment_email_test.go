package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type privateEmailRepoStub struct {
	*transactionRepoStub
	claims  int
	failure string
	finish  bool
}

func (r *privateEmailRepoStub) ListPrivatePaymentEmails(_ context.Context, now time.Time, _ int) ([]domain.Transaction, error) {
	if validPrivatePaymentEmail(r.transaction, now) && r.transaction.PrivatePaymentEmailClaimedAt == nil {
		return []domain.Transaction{*r.transaction}, nil
	}
	return nil, nil
}
func (r *privateEmailRepoStub) ClaimPrivatePaymentEmail(_ context.Context, id uuid.UUID, now time.Time, timeout time.Duration) (bool, error) {
	tx := r.transaction
	if tx.ID != id || !validPrivatePaymentEmail(tx, now) || tx.PrivatePaymentEmailClaimedAt != nil && tx.PrivatePaymentEmailClaimedAt.After(now.Add(-timeout)) {
		return false, nil
	}
	r.claims++
	tx.PrivatePaymentEmailClaimedAt = &now
	tx.PrivatePaymentEmailFailureReason = nil
	return true, nil
}
func (r *privateEmailRepoStub) FinishPrivatePaymentEmail(_ context.Context, id uuid.UUID, claimedAt time.Time, failure string) (bool, error) {
	tx := r.transaction
	if tx.ID != id || tx.Status != domain.TransactionStatusPending || tx.PrivatePaymentEmailClaimedAt == nil || !tx.PrivatePaymentEmailClaimedAt.Equal(claimedAt) {
		return false, nil
	}
	r.finish = true
	tx.PrivatePaymentEmailClaimedAt = nil
	r.failure = failure
	if failure == "" {
		tx.PrivatePaymentEmailSentAt = &claimedAt
	} else {
		tx.PrivatePaymentEmailFailureReason = &failure
	}
	return true, nil
}

type privateEmailSender struct {
	messages []domain.EmailMessage
	err      error
	onSend   func()
}

func (e *privateEmailSender) Send(_ context.Context, m domain.EmailMessage) error {
	e.messages = append(e.messages, m)
	if e.onSend != nil {
		e.onSend()
	}
	return e.err
}
func privateEmailFixture() (*privateEmailRepoStub, *domain.GenerateSubscriptionPaymentRequest, *invoiceGatewayStub) {
	r := &privateEmailRepoStub{transactionRepoStub: &transactionRepoStub{missing: true}}
	req := &domain.GenerateSubscriptionPaymentRequest{EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), ClassID: uuid.New(), BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000, SenderEmail: "parent@example.test", PrivateScheduleRequest: true}
	return r, req, &invoiceGatewayStub{}
}
func privateInvoiceUsecase(r *privateEmailRepoStub, gateway *invoiceGatewayStub, sender *privateEmailSender) TransactionUsecase {
	u := newTransactionUsecaseForTest(r.transactionRepoStub, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, claimTestConfig()).(*transactionUsecase)
	u.txRepo = r
	u.outcomeEmail = sender
	return u
}
func TestPrivateApprovalInvoiceEmailReplayAndRetry(t *testing.T) {
	r, req, gateway := privateEmailFixture()
	sender := &privateEmailSender{}
	u := privateInvoiceUsecase(r, gateway, sender)
	first, err := u.GenerateSubscriptionPayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if r.createCount != 1 || len(gateway.requests) != 1 || len(sender.messages) != 1 || r.transaction.PrivatePaymentEmailSentAt == nil {
		t.Fatalf("invoice=%d provider=%d emails=%d sent=%v", r.createCount, len(gateway.requests), len(sender.messages), r.transaction.PrivatePaymentEmailSentAt)
	}
	m := sender.messages[0]
	if m.To != "parent@example.test" || !strings.Contains(m.HTML, first.CheckoutSessionURL) || !strings.Contains(m.HTML, "IDR 40000") || !strings.Contains(m.HTML, r.transaction.InvoiceExpiresAt.UTC().Format("2006-01-02 15:04 MST")) || m.IdempotencyKey == "" {
		t.Fatalf("incorrect payment email: %+v", m)
	}
	req.SenderEmail = "attacker@example.test"
	second, err := u.GenerateSubscriptionPayment(context.Background(), req)
	if err != nil || second.TransactionID != first.TransactionID || second.CheckoutSessionURL != first.CheckoutSessionURL || len(sender.messages) != 1 || len(gateway.requests) != 1 {
		t.Fatalf("replay response=%+v err=%v emails=%d invoices=%d", second, err, len(sender.messages), len(gateway.requests))
	}
}
func TestPrivateApprovalEmailFailureIsRetryable(t *testing.T) {
	r, req, gateway := privateEmailFixture()
	sender := &privateEmailSender{err: errors.New("resend unavailable")}
	u := privateInvoiceUsecase(r, gateway, sender)
	response, err := u.GenerateSubscriptionPayment(context.Background(), req)
	if err != nil || response == nil || r.transaction.Status != domain.TransactionStatusPending || r.transaction.PrivatePaymentEmailClaimedAt != nil || r.transaction.PrivatePaymentEmailFailureReason == nil || !strings.Contains(*r.transaction.PrivatePaymentEmailFailureReason, "resend unavailable") {
		t.Fatalf("response=%+v err=%v tx=%+v", response, err, r.transaction)
	}
	sender.err = nil
	_, err = u.GenerateSubscriptionPayment(context.Background(), req)
	if err != nil || len(sender.messages) != 2 || sender.messages[0].IdempotencyKey != sender.messages[1].IdempotencyKey || r.transaction.PrivatePaymentEmailSentAt == nil || len(gateway.requests) != 1 {
		t.Fatalf("retry err=%v sent=%d invoiced=%d", err, len(sender.messages), len(gateway.requests))
	}
}
func TestPrivatePaymentEmailWorkerRetriesWithoutApprovalReplay(t *testing.T) {
	r, req, gateway := privateEmailFixture()
	sender := &privateEmailSender{err: errors.New("temporary outage")}
	u := privateInvoiceUsecase(r, gateway, sender)
	if _, err := u.GenerateSubscriptionPayment(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if r.transaction.PrivatePaymentEmailFailureReason == nil {
		t.Fatal("missing delivery failure")
	}
	sender.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender.onSend = cancel
	RunPrivatePaymentEmails(ctx, r, sender, time.Minute)
	if len(sender.messages) != 2 || r.transaction.PrivatePaymentEmailSentAt == nil || r.transaction.PrivatePaymentEmailFailureReason != nil || len(gateway.requests) != 1 {
		t.Fatalf("worker retry: emails=%d sent=%v failure=%v invoices=%d", len(sender.messages), r.transaction.PrivatePaymentEmailSentAt, r.transaction.PrivatePaymentEmailFailureReason, len(gateway.requests))
	}
}

func TestPrivatePaymentEmailGuardsAndCrashRecovery(t *testing.T) {
	r, req, gateway := privateEmailFixture()
	sender := &privateEmailSender{}
	u := privateInvoiceUsecase(r, gateway, sender)
	_, err := u.GenerateSubscriptionPayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	tx := r.transaction
	tx.PrivatePaymentEmailSentAt = nil
	firstKey := sender.messages[0].IdempotencyKey
	crashedAt := time.Now().Add(-11 * time.Minute)
	tx.PrivatePaymentEmailClaimedAt = &crashedAt
	dispatchPrivatePaymentEmail(context.Background(), r, sender, tx.ID)
	if len(sender.messages) != 2 || sender.messages[1].IdempotencyKey != firstKey {
		t.Fatalf("crash recovery emails=%+v", sender.messages)
	}
	cases := []struct {
		name   string
		change func()
	}{
		{"paid callback", func() { tx.Status = domain.TransactionStatusPaid }},
		{"cancelled", func() { tx.Status = domain.TransactionStatusCancelled }},
		{"expired", func() { tx.InvoiceExpiresAt = &crashedAt }},
		{"missing destination", func() { tx.BillingEmail = "" }},
		{"invalid destination", func() { tx.BillingEmail = "bad address" }},
		{"unsafe URL", func() { tx.CheckoutSessionURL = strPtr("javascript:alert(1)") }},
		{"not private", func() { tx.PrivateScheduleRequest = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := *tx
			tx.PrivatePaymentEmailSentAt = nil
			tc.change()
			count := len(sender.messages)
			dispatchPrivatePaymentEmail(context.Background(), r, sender, tx.ID)
			if len(sender.messages) != count {
				t.Fatal("sent an invalid invoice")
			}
			*tx = original
		})
	}
}
