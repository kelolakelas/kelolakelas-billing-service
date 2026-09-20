package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func newTransactionMock(t *testing.T) (*transactionRepository, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return &transactionRepository{db: db}, mock, func() { sqlDB.Close() }
}

// Expiry is the trigger for releasing a seat, so the statement that flips a transaction
// to expired must also enqueue the release job for its enrollment in the same statement.
// The arguments are asserted positionally because the safety of the whole feature rests on
// the inserted row carrying kind="release": an activation job here would confirm a seat the
// parent never paid for.
func TestExpireDueEnqueuesReleaseJobForExpiredTransactions(t *testing.T) {
	now := time.Date(2026, time.September, 20, 10, 0, 0, 0, time.UTC)
	firstID, secondID := uuid.New(), uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	expiredStatement := regexp.QuoteMeta("INSERT INTO payment_reconciliations")
	mock.ExpectQuery(expiredStatement).
		WithArgs(
			domain.TransactionStatusPending, now, 50,
			domain.TransactionStatusExpired, now, now, domain.TransactionStatusPending,
			domain.ReconciliationKindRelease, domain.ReconciliationStatusPending, now, now, now,
			uuid.Nil,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(firstID).AddRow(secondID))

	count, err := repo.ExpireDue(context.Background(), now, 50)
	if err != nil {
		t.Fatalf("ExpireDue error: %v", err)
	}
	if count != 2 {
		t.Fatalf("ExpireDue count = %d, want 2", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A non-positive batch size must never touch the database: the expiry worker calls the
// repository from a drain loop and has to be able to stop without side effects.
func TestExpireDueSkipsWorkForNonPositiveLimit(t *testing.T) {
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	count, err := repo.ExpireDue(context.Background(), time.Now(), 0)
	if err != nil {
		t.Fatalf("ExpireDue error: %v", err)
	}
	if count != 0 {
		t.Fatalf("ExpireDue count = %d, want 0", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
