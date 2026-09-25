package usecase

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type emailClaimRepoStub struct {
	*transactionRepoStub
	persisted *domain.Transaction
	released  bool
}

func (r *emailClaimRepoStub) ClaimPaymentLinkEmail(_ context.Context, id uuid.UUID, sentAt time.Time) (bool, error) {
	if r.persisted.ID != id || r.persisted.Status != domain.TransactionStatusPending || r.persisted.PaymentLinkSentAt != nil {
		return false, nil
	}
	r.persisted.PaymentLinkSentAt = &sentAt
	return true, nil
}

func (r *emailClaimRepoStub) ReleasePaymentLinkEmailClaim(_ context.Context, id uuid.UUID, sentAt time.Time) (bool, error) {
	if r.persisted.ID != id || r.persisted.Status != domain.TransactionStatusPending ||
		r.persisted.PaymentLinkSentAt == nil || !r.persisted.PaymentLinkSentAt.Equal(sentAt) {
		return false, nil
	}
	r.persisted.PaymentLinkSentAt = nil
	r.released = true
	return true, nil
}

type failingEmailStub struct {
	onSend func()
}

func (e failingEmailStub) Send(context.Context, domain.EmailMessage) error {
	if e.onSend != nil {
		e.onSend()
	}
	return errors.New("resend unavailable")
}

func TestSubscriptionWorkerFailedEmailDoesNotRewritePaidTransaction(t *testing.T) {
	for _, tc := range []struct {
		name           string
		payBeforeError bool
	}{
		{name: "pending claim is released"},
		{name: "paid callback wins before email failure", payBeforeError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			tx := claimTestTransaction(uuid.New())
			link := "https://pay.example.com/invoice"
			tx.CheckoutSessionURL = &link
			tx.BillingEmail = "parent@example.com"
			sub := subscriptionForRenewal(tx, uuid.New())
			// Model a stale worker snapshot, separate from the row a paid callback mutates.
			snapshot := *tx
			repo := &emailClaimRepoStub{transactionRepoStub: &transactionRepoStub{transaction: &snapshot, periodTx: &snapshot}, persisted: tx}
			email := failingEmailStub{onSend: func() {
				if tc.payBeforeError {
					tx.Status = domain.TransactionStatusPaid
					tx.PaidAt = &now
				}
			}}
			worker := NewSubscriptionWorker(&renewalSubscriptionRepoStub{subscription: sub}, repo, &invoiceGatewayStub{}, email, claimTestConfig(), renewalClock{now: now})
			worker.RunOnce(context.Background())
			if repo.updateCount != 0 {
				t.Fatalf("full-row writes = %d, want 0", repo.updateCount)
			}
			if tc.payBeforeError {
				if tx.Status != domain.TransactionStatusPaid || tx.PaidAt == nil || tx.PaymentLinkSentAt == nil || repo.released {
					t.Fatalf("paid transaction was changed: status=%s paidAt=%v sentAt=%v released=%v", tx.Status, tx.PaidAt, tx.PaymentLinkSentAt, repo.released)
				}
			} else if tx.PaymentLinkSentAt != nil || !repo.released {
				t.Fatalf("pending claim not released: sentAt=%v released=%v", tx.PaymentLinkSentAt, repo.released)
			}
		})
	}
}

type failingRenewalListingStub struct {
	*renewalSubscriptionRepoStub
}

func (s *failingRenewalListingStub) ListDueForRenewal(context.Context, time.Time) ([]domain.Subscription, error) {
	return nil, errors.New("listing unavailable")
}

func TestSubscriptionWorkerLogsListingAndPerSubscriptionFailure(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	worker := NewSubscriptionWorker(&failingRenewalListingStub{renewalSubscriptionRepoStub: &renewalSubscriptionRepoStub{}}, nil, nil, nil, claimTestConfig())
	worker.RunOnce(context.Background())
	if !strings.Contains(output.String(), "list due subscriptions for renewal failed") || !strings.Contains(output.String(), "listing unavailable") {
		t.Fatalf("listing failure missing from log: %s", output.String())
	}

	output.Reset()
	tx := claimTestTransaction(uuid.New())
	sub := subscriptionForRenewal(tx, uuid.New())
	// A lookup failure is returned from process and must be logged with the subscription id.
	worker = NewSubscriptionWorker(&renewalSubscriptionRepoStub{subscription: sub}, &transactionRepoStub{}, nil, nil, claimTestConfig(), renewalClock{now: time.Now()})
	worker.RunOnce(context.Background())
	if !strings.Contains(output.String(), sub.ID.String()) || !strings.Contains(output.String(), "subscription_id") || !strings.Contains(output.String(), "process subscription renewal failed") {
		t.Fatalf("renewal failure missing id from log: %s", output.String())
	}
}
