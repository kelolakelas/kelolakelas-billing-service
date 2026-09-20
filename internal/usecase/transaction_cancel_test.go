package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func cancelTestTransaction(status string) *domain.Transaction {
	return &domain.Transaction{
		ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(),
		StudentID: uuid.New(), EnrollmentID: uuid.New(), GrossAmount: 40000, NetAmount: 38000,
		Currency: "IDR", Status: status, IsSandbox: true,
		CheckoutSessionURL: strPtr("https://pay.example.com/live"), PaymentIntentID: strPtr("REF-LIVE"),
	}
}

func TestCancelEnrollmentPaymentCancelsUnpaidTransaction(t *testing.T) {
	tx := cancelTestTransaction(domain.TransactionStatusPending)
	repo := &transactionRepoStub{transaction: tx}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())

	response, err := usecase.CancelEnrollmentPayment(context.Background(), tx.EnrollmentID)
	if err != nil {
		t.Fatalf("CancelEnrollmentPayment() error = %v", err)
	}
	if response.Status != domain.TransactionStatusCancelled {
		t.Fatalf("status=%q, want %q", response.Status, domain.TransactionStatusCancelled)
	}
	if tx.Status != domain.TransactionStatusCancelled {
		t.Fatalf("persisted status=%q, want %q", tx.Status, domain.TransactionStatusCancelled)
	}
}

// A transaction that never got a payment link is still cancellable, which is the
// enrollment-without-transaction edge case: the parent must not be blocked by a
// billing row that was created but never completed.
func TestCancelEnrollmentPaymentCancelsTransactionStuckInCreating(t *testing.T) {
	tx := cancelTestTransaction(domain.TransactionStatusCreating)
	tx.CheckoutSessionURL = nil
	repo := &transactionRepoStub{transaction: tx}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())

	response, err := usecase.CancelEnrollmentPayment(context.Background(), tx.EnrollmentID)
	if err != nil {
		t.Fatalf("CancelEnrollmentPayment() error = %v", err)
	}
	if response.Status != domain.TransactionStatusCancelled {
		t.Fatalf("status=%q, want %q", response.Status, domain.TransactionStatusCancelled)
	}
}

// Repeating the withdrawal converges: academic retries after a partial failure, and a
// second attempt must not be reported as a conflict for a state it already reached.
func TestCancelEnrollmentPaymentIsIdempotentForCancelledTransaction(t *testing.T) {
	tx := cancelTestTransaction(domain.TransactionStatusCancelled)
	repo := &transactionRepoStub{transaction: tx}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())

	response, err := usecase.CancelEnrollmentPayment(context.Background(), tx.EnrollmentID)
	if err != nil {
		t.Fatalf("CancelEnrollmentPayment() error = %v", err)
	}
	if response.Status != domain.TransactionStatusCancelled {
		t.Fatalf("status=%q, want %q", response.Status, domain.TransactionStatusCancelled)
	}
	if repo.updateCount != 0 {
		t.Fatalf("update count=%d, want a repeated cancellation to write nothing", repo.updateCount)
	}
}

// A paid transaction must never be rewritten by a cancellation: the parent paid, so
// the seat stays theirs and the refusal must be visible to academic.
func TestCancelEnrollmentPaymentNeverRewritesPaidTransaction(t *testing.T) {
	for _, status := range []string{domain.TransactionStatusPaid, domain.TransactionStatusRefunded} {
		t.Run(status, func(t *testing.T) {
			tx := cancelTestTransaction(status)
			repo := &transactionRepoStub{transaction: tx}
			usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())

			_, err := usecase.CancelEnrollmentPayment(context.Background(), tx.EnrollmentID)
			if !errors.Is(err, domain.ErrInvalidTransactionStatus) {
				t.Fatalf("error=%v, want ErrInvalidTransactionStatus", err)
			}
			if tx.Status != status {
				t.Fatalf("status=%q, want %q preserved", tx.Status, status)
			}
		})
	}
}

func TestCancelEnrollmentPaymentReportsUnknownEnrollment(t *testing.T) {
	repo := &transactionRepoStub{missing: true}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())

	_, err := usecase.CancelEnrollmentPayment(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrTransactionNotFound) {
		t.Fatalf("error=%v, want ErrTransactionNotFound", err)
	}
}

func TestCancelEnrollmentPaymentRejectsNilEnrollment(t *testing.T) {
	repo := &transactionRepoStub{missing: true}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())

	_, err := usecase.CancelEnrollmentPayment(context.Background(), uuid.Nil)
	if !errors.Is(err, domain.ErrTransactionNotFound) {
		t.Fatalf("error=%v, want ErrTransactionNotFound", err)
	}
}

// The withdrawal hands the seat back through the atomic statement, which owns the
// release job. Delegating to it rather than writing the status directly is what keeps
// the seat and the transaction consistent in a single round trip.
func TestCancelEnrollmentPaymentUsesAtomicWithdrawal(t *testing.T) {
	tx := cancelTestTransaction(domain.TransactionStatusPending)
	repo := &transactionRepoStub{transaction: tx}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())

	if _, err := usecase.CancelEnrollmentPayment(context.Background(), tx.EnrollmentID); err != nil {
		t.Fatalf("CancelEnrollmentPayment() error = %v", err)
	}
	if repo.cancelUnpaidRuns != 1 {
		t.Fatalf("atomic withdrawals=%d, want 1", repo.cancelUnpaidRuns)
	}
	if repo.updateCount != 0 {
		t.Fatalf("update count=%d, want the atomic statement to own the write", repo.updateCount)
	}
}

// Acceptance criterion: a paid callback that arrives after the cancellation is still
// honoured as a real payment. The transaction becomes `paid` so the money is visible,
// while the activation is rejected by Academic because the enrollment is dropped. The
// rejection is recorded, not swallowed, so the paid-but-dropped seat stays observable.
func TestPaidCallbackAfterCancellationIsRecordedWithoutReactivating(t *testing.T) {
	enrollmentID := uuid.New()
	tx := cancelTestTransaction(domain.TransactionStatusPending)
	tx.EnrollmentID = enrollmentID
	repo := &transactionRepoStub{transaction: tx}
	reconciliation := &domain.PaymentReconciliation{
		ID: uuid.New(), TransactionID: tx.ID, EnrollmentID: enrollmentID,
		Kind: domain.ReconciliationKindActivation, AttemptCount: 1,
	}
	reconciliationRepo := &reconciliationRepoStub{item: reconciliation}
	academicStub := &academicActivationStub{err: errors.New("academic service returned status code 409: enrollment is not pending")}
	usecase := newTransactionUsecaseForTest(repo, reconciliationRepo, &invoiceGatewayStub{}, academicStub, expiryTestConfig())

	// The parent cancels first: the transaction is withdrawn and the seat is released.
	if _, err := usecase.CancelEnrollmentPayment(context.Background(), enrollmentID); err != nil {
		t.Fatalf("CancelEnrollmentPayment() error = %v", err)
	}
	if tx.Status != domain.TransactionStatusCancelled {
		t.Fatalf("status=%q, want %q after cancellation", tx.Status, domain.TransactionStatusCancelled)
	}

	// The invoice is still payable at the provider, so the payment lands afterwards.
	if err := usecase.HandleDuitkuWebhook(context.Background(), callbackPayload(tx, domain.ResultCodeSuccess)); err != nil {
		t.Fatalf("HandleDuitkuWebhook() error = %v", err)
	}
	if tx.Status != domain.TransactionStatusPaid {
		t.Fatalf("status=%q, want the payment recorded as %q", tx.Status, domain.TransactionStatusPaid)
	}

	// Academic refuses the activation because the enrollment is dropped. The refusal is
	// what must stay visible instead of being retried into a silent success.
	if academicStub.calls != 1 {
		t.Fatalf("activation attempts=%d, want exactly 1", academicStub.calls)
	}
	if academicStub.releaseCalls != 0 {
		t.Fatalf("release calls=%d, want the paid callback to attempt activation only", academicStub.releaseCalls)
	}
	if reconciliationRepo.active {
		t.Fatal("the activation must not be confirmed for a dropped enrollment")
	}
	if !reconciliationRepo.retry || reconciliationRepo.lastError == "" {
		t.Fatalf("retry=%v lastError=%q, want the rejection recorded", reconciliationRepo.retry, reconciliationRepo.lastError)
	}

	// The recorded error is what a parent or operator sees on the transaction.
	reconciliation.LastError = &reconciliationRepo.lastError
	reconciliation.Status = domain.ReconciliationStatusProcessing
	tx.Reconciliation = reconciliation
	response := transactionResponse(tx)
	if response.ReconciliationLastError == "" {
		t.Fatal("the rejected activation must be visible on the transaction")
	}
	if response.Status != domain.TransactionStatusPaid {
		t.Fatalf("response status=%q, want %q so the payment is visible", response.Status, domain.TransactionStatusPaid)
	}
}

// An enrollment that is already paid must not have its invoice withdrawn, so the
// cancellation is refused before any write happens.
func TestCancelEnrollmentPaymentDoesNotClearPaymentLinkOnRefusal(t *testing.T) {
	tx := cancelTestTransaction(domain.TransactionStatusPaid)
	repo := &transactionRepoStub{transaction: tx}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())

	_, err := usecase.CancelEnrollmentPayment(context.Background(), tx.EnrollmentID)
	if !errors.Is(err, domain.ErrInvalidTransactionStatus) {
		t.Fatalf("error=%v, want ErrInvalidTransactionStatus", err)
	}
	if tx.CheckoutSessionURL == nil || tx.PaymentIntentID == nil {
		t.Fatal("a refused cancellation must leave the payment references intact")
	}
	if repo.updateCount != 0 {
		t.Fatalf("update count=%d, want no write for a refused cancellation", repo.updateCount)
	}
}

// A repository without the atomic conditional update still behaves consistently: the
// fallback transition must cancel the row rather than leave it payable.
func TestCancelEnrollmentPaymentFallsBackWithoutLockingRepository(t *testing.T) {
	tx := cancelTestTransaction(domain.TransactionStatusPending)
	repo := &plainTransactionRepoStub{transaction: tx}
	usecase := NewTransactionUsecaseWithReconciliation(
		repo, &walletRepoStub{}, &ledgerRepoStub{}, &subscriptionRepoStub{}, &invoiceGatewayStub{},
		&academicActivationStub{}, expiryTestConfig(), nil, &reconciliationRepoStub{},
	)

	response, err := usecase.CancelEnrollmentPayment(context.Background(), tx.EnrollmentID)
	if err != nil {
		t.Fatalf("CancelEnrollmentPayment() error = %v", err)
	}
	if response.Status != domain.TransactionStatusCancelled || tx.Status != domain.TransactionStatusCancelled {
		t.Fatalf("response=%q persisted=%q, want %q", response.Status, tx.Status, domain.TransactionStatusCancelled)
	}
}

// plainTransactionRepoStub deliberately does not implement
// repository.TransactionLockingRepository, so the in-memory fallback path is covered.
type plainTransactionRepoStub struct {
	transaction *domain.Transaction
}

func (r *plainTransactionRepoStub) Create(context.Context, *domain.Transaction) error { return nil }
func (r *plainTransactionRepoStub) GetByID(context.Context, uuid.UUID) (*domain.Transaction, error) {
	if r.transaction == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return r.transaction, nil
}
func (r *plainTransactionRepoStub) GetByMerchantOrderID(context.Context, string) (*domain.Transaction, error) {
	return r.GetByID(context.Background(), uuid.Nil)
}
func (r *plainTransactionRepoStub) GetByEnrollmentID(context.Context, uuid.UUID) (*domain.Transaction, error) {
	return r.GetByID(context.Background(), uuid.Nil)
}
func (r *plainTransactionRepoStub) GetBySubscriptionPeriod(context.Context, uuid.UUID, time.Time) (*domain.Transaction, error) {
	return nil, gorm.ErrRecordNotFound
}
func (r *plainTransactionRepoStub) GetByPaymentIntentID(context.Context, string) (*domain.Transaction, error) {
	return nil, gorm.ErrRecordNotFound
}
func (r *plainTransactionRepoStub) List(context.Context, *uuid.UUID, *uuid.UUID, domain.TransactionQuery) ([]domain.Transaction, int64, error) {
	return nil, 0, nil
}
func (r *plainTransactionRepoStub) Update(_ context.Context, tx *domain.Transaction) error {
	r.transaction = tx
	return nil
}
