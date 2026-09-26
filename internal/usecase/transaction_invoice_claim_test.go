package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// failingInvoiceGatewayStub simulates a provider that refuses to create invoices. The
// first `failures` calls report the error and every later call succeeds, which is how
// the tests model a transient gateway outage followed by a healthy retry.
type failingInvoiceGatewayStub struct {
	failures int
	requests []*domain.CreateInvoiceRequest
}

func (g *failingInvoiceGatewayStub) CreateInvoice(_ context.Context, request *domain.CreateInvoiceRequest) (*domain.CreateInvoiceResponse, error) {
	g.requests = append(g.requests, request)
	if len(g.requests) <= g.failures {
		return nil, errors.New("duitku unavailable")
	}
	return &domain.CreateInvoiceResponse{
		Reference:  "REF-" + uuid.NewString(),
		PaymentURL: "https://pay.example.com/" + uuid.NewString(),
	}, nil
}

func (g *failingInvoiceGatewayStub) ValidateCallbackSignature(*domain.DuitkuCallbackPayload) bool {
	return true
}
func (g *failingInvoiceGatewayStub) TransactionStatus(context.Context, string) (*domain.PaymentStatus, error) {
	return nil, errors.New("duitku unavailable")
}

func claimTestConfig() config.Config {
	return config.Config{
		SubscriptionPaymentExpiryPeriodDays: 7,
		PaymentReconciliationMaxAttempts:    3,
		TransactionClaimTimeoutMinutes:      10,
	}
}

func claimTestTransaction(enrollmentID uuid.UUID) *domain.Transaction {
	return &domain.Transaction{
		ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(),
		StudentID: enrollmentID, EnrollmentID: enrollmentID, GrossAmount: 40000, NetAmount: 38000,
		Currency: "IDR", Status: domain.TransactionStatusPending, IsSandbox: true,
	}
}

func claimTestRequest(tx *domain.Transaction) *domain.GenerateSubscriptionPaymentRequest {
	return &domain.GenerateSubscriptionPaymentRequest{
		EnrollmentID: tx.EnrollmentID, TenantID: tx.TenantID, ParentID: tx.ParentID,
		BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000,
	}
}

// Acceptance criterion (a): after a simulated provider failure the transaction must be
// claimable again, so the very next request with the same enrollment creates the invoice
// instead of failing forever on a row stuck in `creating`.
func TestFailedInvoiceCreationLeavesTransactionClaimableForTheNextRequest(t *testing.T) {
	enrollmentID := uuid.New()
	tx := claimTestTransaction(enrollmentID)
	repo := &transactionRepoStub{transaction: tx}
	gateway := &failingInvoiceGatewayStub{failures: 1}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, claimTestConfig())

	if _, err := usecase.GenerateSubscriptionPayment(context.Background(), claimTestRequest(tx)); err == nil {
		t.Fatal("GenerateSubscriptionPayment() error = nil, want the provider failure to surface")
	}
	if tx.Status != domain.TransactionStatusFailed {
		t.Fatalf("status after the failure = %q, want %q so the next request may claim it", tx.Status, domain.TransactionStatusFailed)
	}
	if tx.InvoiceFailureReason == nil || !strings.Contains(*tx.InvoiceFailureReason, "duitku unavailable") {
		t.Fatalf("invoice_failure_reason = %v, want the provider error recorded", tx.InvoiceFailureReason)
	}
	if tx.InvoiceClaimedAt != nil {
		t.Fatalf("invoice_claimed_at = %v, want the claim cleared on release", tx.InvoiceClaimedAt)
	}
	if repo.restoreRuns != 1 {
		t.Fatalf("claim releases = %d, want exactly 1 on the error path", repo.restoreRuns)
	}

	// The enrollment is not blocked: the retry succeeds and replaces the failure reason.
	response, err := usecase.GenerateSubscriptionPayment(context.Background(), claimTestRequest(tx))
	if err != nil {
		t.Fatalf("retry GenerateSubscriptionPayment() error = %v, want a successful retry", err)
	}
	if response.CheckoutSessionURL == "" {
		t.Fatal("retry returned no checkout session URL")
	}
	if tx.Status != domain.TransactionStatusPending {
		t.Fatalf("status after the retry = %q, want %q", tx.Status, domain.TransactionStatusPending)
	}
	if tx.InvoiceFailureReason != nil {
		t.Fatalf("invoice_failure_reason = %v, want the successful retry to clear it", *tx.InvoiceFailureReason)
	}
	if len(gateway.requests) != 2 {
		t.Fatalf("gateway calls = %d, want 2 (the failed attempt and the successful retry)", len(gateway.requests))
	}
}

// Acceptance criterion (b): the claim stays exclusive, so two requests that race for the
// same enrollment produce exactly one invoice. The second caller reads the state the
// winner is producing rather than calling the provider again.
func TestParallelInvoiceRequestsProduceExactlyOneInvoice(t *testing.T) {
	enrollmentID := uuid.New()
	tx := claimTestTransaction(enrollmentID)
	repo := &transactionRepoStub{transaction: tx}
	gateway := &invoiceGatewayStub{}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, claimTestConfig())

	// The winner claims the row first, which is what the atomic UPDATE does in the database.
	if _, err := usecase.GenerateSubscriptionPayment(context.Background(), claimTestRequest(tx)); err != nil {
		t.Fatalf("winner GenerateSubscriptionPayment() error = %v", err)
	}
	if tx.Status != domain.TransactionStatusPending {
		t.Fatalf("status after the winner = %q, want %q", tx.Status, domain.TransactionStatusPending)
	}
	invoicesAfterWinner := len(gateway.requests)

	// The loser arrives while the winner's claim is still fresh. Its claim must fail, and
	// it must answer with the link the winner produced instead of creating a second one.
	repo.claimUnavailable = true
	response, err := usecase.GenerateSubscriptionPayment(context.Background(), claimTestRequest(tx))
	if err != nil {
		t.Fatalf("loser GenerateSubscriptionPayment() error = %v, want the winner's invoice reused", err)
	}
	if len(gateway.requests) != invoicesAfterWinner {
		t.Fatalf("gateway calls = %d, want %d: the loser must not create a second invoice", len(gateway.requests), invoicesAfterWinner)
	}
	if response.CheckoutSessionURL != *tx.CheckoutSessionURL {
		t.Fatalf("loser checkout session URL = %q, want the winner's %q", response.CheckoutSessionURL, *tx.CheckoutSessionURL)
	}
}

// Acceptance criterion (c): a transaction abandoned in `creating` by a process that died
// becomes claimable again once the configured timeout has passed.
func TestStaleInvoiceClaimIsReclaimableAfterTheTimeout(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name         string
		claimedAt    time.Time
		wantClaimed  bool
		wantRequests int
	}{
		{
			name:         "claim older than the timeout is reclaimed",
			claimedAt:    now.Add(-11 * time.Minute),
			wantClaimed:  true,
			wantRequests: 1,
		},
		{
			name:         "claim inside the timeout is left alone",
			claimedAt:    now.Add(-2 * time.Minute),
			wantClaimed:  false,
			wantRequests: 0,
		},
		{
			name:         "claim with no timestamp is treated as abandoned",
			claimedAt:    time.Time{},
			wantClaimed:  true,
			wantRequests: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			enrollmentID := uuid.New()
			tx := claimTestTransaction(enrollmentID)
			tx.Status = domain.TransactionStatusCreating
			if !test.claimedAt.IsZero() {
				tx.InvoiceClaimedAt = &test.claimedAt
			}
			repo := &transactionRepoStub{transaction: tx}
			gateway := &invoiceGatewayStub{}
			usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, claimTestConfig())

			_, err := usecase.GenerateSubscriptionPayment(context.Background(), claimTestRequest(tx))
			if test.wantClaimed && err != nil {
				t.Fatalf("GenerateSubscriptionPayment() error = %v, want the abandoned claim to be recovered", err)
			}
			if !test.wantClaimed && err == nil {
				t.Fatal("GenerateSubscriptionPayment() error = nil, want the live claim to be respected")
			}
			if len(gateway.requests) != test.wantRequests {
				t.Fatalf("gateway calls = %d, want %d", len(gateway.requests), test.wantRequests)
			}
		})
	}
}

// Edge case: a response that was lost on the way back must never produce a second invoice
// for the same merchant order id, so a row that already holds a payment link is never
// reclaimed even when its claim looks abandoned.
func TestLostProviderResponseDoesNotProduceASecondInvoice(t *testing.T) {
	enrollmentID := uuid.New()
	claimedAt := time.Now().Add(-time.Hour)
	link := "https://pay.example.com/already-issued"
	tx := claimTestTransaction(enrollmentID)
	tx.Status = domain.TransactionStatusCreating
	tx.CheckoutSessionURL = &link
	tx.PaymentIntentID = strPtr("REF-ISSUED")
	tx.InvoiceClaimedAt = &claimedAt

	repo := &transactionRepoStub{transaction: tx}
	gateway := &invoiceGatewayStub{}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, claimTestConfig())

	response, err := usecase.GenerateSubscriptionPayment(context.Background(), claimTestRequest(tx))
	if err != nil {
		t.Fatalf("GenerateSubscriptionPayment() error = %v, want the stored link to be reused", err)
	}
	if len(gateway.requests) != 0 {
		t.Fatalf("gateway calls = %d, want 0: an already issued invoice must not be recreated", len(gateway.requests))
	}
	if response.CheckoutSessionURL != link {
		t.Fatalf("checkout session URL = %q, want the stored %q", response.CheckoutSessionURL, link)
	}
	if tx.Status != domain.TransactionStatusCreating {
		t.Fatalf("status = %q, want %q untouched", tx.Status, domain.TransactionStatusCreating)
	}
}

// Edge case: when the release write itself fails the provider error must still be the
// error the caller sees, and the timeout remains the safety net for the stuck row.
func TestFailedClaimReleaseStillReportsTheProviderError(t *testing.T) {
	enrollmentID := uuid.New()
	tx := claimTestTransaction(enrollmentID)
	repo := &transactionRepoStub{transaction: tx, restoreErr: errors.New("write conflict")}
	gateway := &failingInvoiceGatewayStub{failures: 1}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, claimTestConfig())

	_, err := usecase.GenerateSubscriptionPayment(context.Background(), claimTestRequest(tx))
	if err == nil {
		t.Fatal("GenerateSubscriptionPayment() error = nil, want the provider failure")
	}
	if !strings.Contains(err.Error(), "duitku unavailable") {
		t.Fatalf("error = %v, want the provider failure to be reported", err)
	}
}

// Edge case: a cancellation that won the race must not be rewritten by the failure
// release, because the parent explicitly stopped paying for the seat.
func TestFailedInvoiceCreationDoesNotRewriteACancelledTransaction(t *testing.T) {
	enrollmentID := uuid.New()
	tx := claimTestTransaction(enrollmentID)
	tx.Status = domain.TransactionStatusCancelled
	repo := &transactionRepoStub{transaction: tx, restoreErr: nil}
	gateway := &failingInvoiceGatewayStub{failures: 1}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, claimTestConfig())

	_, err := usecase.GenerateSubscriptionPayment(context.Background(), claimTestRequest(tx))
	if err == nil {
		t.Fatal("GenerateSubscriptionPayment() error = nil, want the cancellation to be respected")
	}
	if tx.Status != domain.TransactionStatusCancelled {
		t.Fatalf("status = %q, want %q preserved", tx.Status, domain.TransactionStatusCancelled)
	}
}

// Acceptance criterion: the subscription worker issues renewal invoices through the same
// exclusive claim, so a renewal that fails is recoverable exactly like a direct request.
func TestSubscriptionWorkerReleasesItsClaimWhenTheRenewalInvoiceFails(t *testing.T) {
	subscriptionID := uuid.New()
	tx := claimTestTransaction(uuid.New())
	tx.Status = domain.TransactionStatusFailed
	dueForRenewal := subscriptionForRenewal(tx, subscriptionID)

	// periodTx makes the worker find the existing row for the period, so the claim and the
	// release under test are applied to the transaction this test inspects.
	repo := &transactionRepoStub{transaction: tx, periodTx: tx}
	gateway := &failingInvoiceGatewayStub{failures: 1}
	worker := NewSubscriptionWorker(&renewalSubscriptionRepoStub{subscription: dueForRenewal}, repo, gateway, nil, claimTestConfig(), renewalClock{now: time.Now()})

	worker.RunOnce(context.Background())

	if repo.restoreRuns != 1 {
		t.Fatalf("claim releases = %d, want exactly 1 so the renewal period is not stranded", repo.restoreRuns)
	}
	if tx.Status != domain.TransactionStatusFailed {
		t.Fatalf("status = %q, want %q so the next run may claim it again", tx.Status, domain.TransactionStatusFailed)
	}
	if tx.InvoiceFailureReason == nil {
		t.Fatal("invoice_failure_reason was not recorded on the failed renewal")
	}
}

// The worker must also respect a claim that another run still owns, otherwise a slow
// renewal and the next scheduled run would both invoice the same billing period.
func TestSubscriptionWorkerSkipsARenewalWhoseClaimIsStillFresh(t *testing.T) {
	subscriptionID := uuid.New()
	now := time.Now()
	tx := claimTestTransaction(uuid.New())
	tx.Status = domain.TransactionStatusCreating
	claimedAt := now.Add(-time.Minute)
	tx.InvoiceClaimedAt = &claimedAt
	dueForRenewal := subscriptionForRenewal(tx, subscriptionID)

	repo := &transactionRepoStub{transaction: tx, periodTx: tx}
	gateway := &invoiceGatewayStub{}
	worker := NewSubscriptionWorker(&renewalSubscriptionRepoStub{subscription: dueForRenewal}, repo, gateway, nil, claimTestConfig(), renewalClock{now: now})

	worker.RunOnce(context.Background())

	if len(gateway.requests) != 0 {
		t.Fatalf("gateway calls = %d, want 0 while another run owns the claim", len(gateway.requests))
	}
	if tx.Status != domain.TransactionStatusCreating || tx.InvoiceClaimedAt == nil {
		t.Fatalf("status=%q claimedAt=%v, want the in-flight claim left untouched", tx.Status, tx.InvoiceClaimedAt)
	}
}

// subscriptionForRenewal builds the subscription the worker is expected to bill, with the
// period anchored to the transaction the repository will hand back so the renewal path
// revisits the same row instead of inserting a second one.
func subscriptionForRenewal(tx *domain.Transaction, subscriptionID uuid.UUID) *domain.Subscription {
	periodStart := time.Now().AddDate(0, 0, 1)
	tx.SubscriptionID = &subscriptionID
	tx.BillingPeriodStart = timePtr(time.Date(periodStart.Year(), periodStart.Month(), periodStart.Day(), 0, 0, 0, 0, time.UTC))
	return &domain.Subscription{
		ID: subscriptionID, EnrollmentID: tx.EnrollmentID, TenantID: tx.TenantID, ParentID: tx.ParentID,
		StudentID: tx.StudentID, BillingCycle: domain.BillingCycleMonthly,
		NextBillingDate: periodStart, Status: "active", Amount: tx.GrossAmount,
	}
}

// renewalSubscriptionRepoStub returns one subscription as due for renewal so the worker
// reaches the invoice creation path under test.
type renewalSubscriptionRepoStub struct {
	subscription *domain.Subscription
}

func (s *renewalSubscriptionRepoStub) Create(context.Context, *domain.Subscription) error { return nil }
func (s *renewalSubscriptionRepoStub) GetByEnrollmentID(context.Context, uuid.UUID) (*domain.Subscription, error) {
	return s.subscription, nil
}
func (s *renewalSubscriptionRepoStub) Update(context.Context, *domain.Subscription) error { return nil }
func (s *renewalSubscriptionRepoStub) ListDueForRenewal(context.Context, time.Time) ([]domain.Subscription, error) {
	return []domain.Subscription{*s.subscription}, nil
}

// renewalClock pins the worker's notion of "now" so the renewal window assertions do not
// depend on how long the test takes to run.
type renewalClock struct{ now time.Time }

func (c renewalClock) Now() time.Time { return c.now }
