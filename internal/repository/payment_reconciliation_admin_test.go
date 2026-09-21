package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func newReconciliationMock(t *testing.T) (*paymentReconciliationRepository, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return &paymentReconciliationRepository{db: db}, mock, func() { sqlDB.Close() }
}

// Requeueing is the operator's only recovery path for a job that exhausted the attempt
// limit, and the status guard is what makes the call safe to replay: only a
// `terminal_failed` row may be moved, so a job that is already pending, in flight, or
// completed can never be reset by a repeated request. The predicate is asserted here
// because a missing status filter would silently restart finished work.
func TestRequeueTerminalFailedOnlyMatchesTerminalRows(t *testing.T) {
	now := time.Date(2026, time.September, 21, 10, 0, 0, 0, time.UTC)
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectBegin()
	// The `status = $4` predicate is the whole safety property: without it a replay would
	// reset jobs that are pending, in flight, or already completed.
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "payment_reconciliations" SET "next_attempt_at"=`)).
		WithArgs(
			now, domain.ReconciliationStatusPending, now,
			domain.ReconciliationStatusTerminalFailed, 10,
		).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	count, err := repo.RequeueTerminalFailed(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("RequeueTerminalFailed error: %v", err)
	}
	if count != 2 {
		t.Fatalf("count=%d, want the 2 rows the database reported", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A re-drive that matches nothing answers zero, which is how an operator can tell the
// first call worked instead of reading a stale success count.
func TestRequeueTerminalFailedReportsZeroWhenNoRowMatches(t *testing.T) {
	now := time.Date(2026, time.September, 21, 10, 0, 0, 0, time.UTC)
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "payment_reconciliations"`)).
		WithArgs(
			now, domain.ReconciliationStatusPending, now,
			domain.ReconciliationStatusTerminalFailed, 10,
		).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	count, err := repo.RequeueTerminalFailed(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("RequeueTerminalFailed error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count=%d, want 0", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A non-positive batch size must not touch the database, so the endpoint cannot be used to
// rewrite an unbounded number of rows.
func TestRequeueTerminalFailedSkipsWorkForNonPositiveLimit(t *testing.T) {
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	count, err := repo.RequeueTerminalFailed(context.Background(), time.Now(), 0)
	if err != nil {
		t.Fatalf("RequeueTerminalFailed error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count=%d, want 0", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The list query has to carry the requested status into the predicate; without it an
// operator filtering for failures would receive every row and could not tell a stuck
// activation from a healthy one.
func TestListByStatusFiltersOnStatus(t *testing.T) {
	enrollmentID := uuid.New()
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "payment_reconciliations" WHERE status = $1`)).
		WithArgs(domain.ReconciliationStatusTerminalFailed, 50).
		WillReturnRows(sqlmock.NewRows([]string{"id", "enrollment_id", "status"}).
			AddRow(uuid.New(), enrollmentID, domain.ReconciliationStatusTerminalFailed))

	items, err := repo.ListByStatus(context.Background(), domain.ReconciliationStatusTerminalFailed, 50)
	if err != nil {
		t.Fatalf("ListByStatus error: %v", err)
	}
	if len(items) != 1 || items[0].EnrollmentID != enrollmentID {
		t.Fatalf("items=%+v, want the filtered row", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// An empty status is the "show me everything" request and must not add a predicate.
func TestListByStatusWithoutFilterReturnsAllRows(t *testing.T) {
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "payment_reconciliations"`)).
		WithArgs(25).
		WillReturnRows(sqlmock.NewRows([]string{"id", "enrollment_id", "status"}))

	items, err := repo.ListByStatus(context.Background(), "", 25)
	if err != nil {
		t.Fatalf("ListByStatus error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items=%d, want 0", len(items))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Enqueueing a release is the transition that makes a seat hand-back durable, so it is
// logged. A conflict means the transaction already owes Academic something else and
// nothing was written, and the log must follow the write rather than the attempt.
func TestEnqueueReleaseLogsOnlyWhenARowWasWritten(t *testing.T) {
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	reconciliation := &domain.PaymentReconciliation{
		ID:            uuid.New(),
		TransactionID: uuid.New(),
		EnrollmentID:  uuid.New(),
		Kind:          domain.ReconciliationKindRelease,
		Status:        domain.ReconciliationStatusPending,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "payment_reconciliations"`)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(reconciliation.ID))
	mock.ExpectCommit()

	if err := repo.EnqueueRelease(context.Background(), reconciliation); err != nil {
		t.Fatalf("EnqueueRelease error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// EnsureActivation must not report a transition when the conflict clause skipped the row,
// otherwise a replayed paid callback would keep announcing a job that already completed.
func TestEnsureActivationDoesNotFailWhenTheRowWasSkipped(t *testing.T) {
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	reconciliation := &domain.PaymentReconciliation{
		ID:            uuid.New(),
		TransactionID: uuid.New(),
		EnrollmentID:  uuid.New(),
		Kind:          domain.ReconciliationKindActivation,
		Status:        domain.ReconciliationStatusPending,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "payment_reconciliations"`)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()

	if err := repo.EnsureActivation(context.Background(), reconciliation); err != nil {
		t.Fatalf("EnsureActivation error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A database failure while reading the claim has to surface unchanged, because the claim
// path decides whether the worker or the paid callback owns the attempt.
func TestClaimDuePropagatesLockError(t *testing.T) {
	failure := errors.New("database unavailable")
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "payment_reconciliations"`)).WillReturnError(failure)
	mock.ExpectRollback()

	if _, err := repo.ClaimDue(context.Background(), uuid.Nil, time.Now(), 5*time.Minute); !errors.Is(err, failure) {
		t.Fatalf("error=%v, want the database error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
