package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// CancelUnpaid is the statement that makes a parent's withdrawal authoritative. Both
// halves of it are asserted here: the status transition only ever matches an unpaid row,
// and the inserted reconciliation row carries kind="release" because the seat must be
// given back. An activation job in this position would confirm a seat the parent has
// explicitly stopped paying for.
func TestCancelUnpaidEmitsConditionalUpdateAndReleaseJob(t *testing.T) {
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	statement := regexp.QuoteMeta("INSERT INTO payment_reconciliations")
	mock.ExpectQuery(statement).
		WithArgs(
			domain.TransactionStatusCancelled, sqlmock.AnyArg(),
			id,
			domain.TransactionStatusPending, domain.TransactionStatusCreating,
			domain.TransactionStatusFailed, domain.TransactionStatusExpired,
			domain.ReconciliationKindRelease, domain.ReconciliationStatusPending,
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			uuid.Nil,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))

	cancelled, err := repo.CancelUnpaid(context.Background(), id)
	if err != nil {
		t.Fatalf("CancelUnpaid error: %v", err)
	}
	if !cancelled {
		t.Fatal("CancelUnpaid reported false while the row was updated")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A settlement that raced the cancellation matches no row. Reporting false is what lets
// the usecase re-read the transaction and refuse the withdrawal instead of telling the
// parent a paid seat was dropped.
func TestCancelUnpaidReportsNoRowMatched(t *testing.T) {
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO payment_reconciliations")).
		WithArgs(
			domain.TransactionStatusCancelled, sqlmock.AnyArg(),
			id,
			domain.TransactionStatusPending, domain.TransactionStatusCreating,
			domain.TransactionStatusFailed, domain.TransactionStatusExpired,
			domain.ReconciliationKindRelease, domain.ReconciliationStatusPending,
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			uuid.Nil,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	cancelled, err := repo.CancelUnpaid(context.Background(), id)
	if err != nil {
		t.Fatalf("CancelUnpaid error: %v", err)
	}
	if cancelled {
		t.Fatal("CancelUnpaid reported true without an updated row")
	}
}

// The statement must also carry the guards that keep it from inventing work: the
// soft-delete filter, and the `enrollment_id <> uuid.Nil` predicate that stops a
// billing-only transaction from queueing a seat release for an enrollment it has not got.
// The regexp matcher asserts each fragment is present in the generated SQL, so dropping
// one of them fails here rather than silently widening what the statement touches.
func TestCancelUnpaidKeepsSoftDeleteAndEnrollmentGuards(t *testing.T) {
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectQuery(`(?s)UPDATE transactions.*deleted_at IS NULL.*enrollment_id <> \$13.*ON CONFLICT \(transaction_id\) DO NOTHING`).
		WithArgs(
			domain.TransactionStatusCancelled, sqlmock.AnyArg(),
			id,
			domain.TransactionStatusPending, domain.TransactionStatusCreating,
			domain.TransactionStatusFailed, domain.TransactionStatusExpired,
			domain.ReconciliationKindRelease, domain.ReconciliationStatusPending,
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			uuid.Nil,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))

	if _, err := repo.CancelUnpaid(context.Background(), id); err != nil {
		t.Fatalf("CancelUnpaid error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// MarkInvoiceIssued is the other half of the cancellation race: an invoice creation that
// finishes after the withdrawal must not put a payment link back on the cancelled row.
func TestMarkInvoiceIssuedRefusesToRewriteTheRowItDoesNotOwn(t *testing.T) {
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	expiresAt := time.Date(2026, time.September, 27, 10, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE \"transactions\"")).
		WithArgs("https://pay.example.com/x", nil, expiresAt, "REF-X", domain.TransactionStatusPending, sqlmock.AnyArg(), id, domain.TransactionStatusCreating, domain.TransactionStatusPending).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	issued, err := repo.MarkInvoiceIssued(context.Background(), id, "https://pay.example.com/x", "REF-X", expiresAt)
	if err != nil {
		t.Fatalf("MarkInvoiceIssued error: %v", err)
	}
	if issued {
		t.Fatal("MarkInvoiceIssued reported true although no row matched")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A repository failure must surface instead of being read as "nothing to cancel".
func TestCancelUnpaidPropagatesRepositoryErrors(t *testing.T) {
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO payment_reconciliations")).
		WillReturnError(errors.New("connection reset"))

	if _, err := repo.CancelUnpaid(context.Background(), id); err == nil {
		t.Fatal("CancelUnpaid swallowed a repository error")
	}
}
