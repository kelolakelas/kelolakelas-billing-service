package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func TestReleasePaymentLinkEmailClaimOnlyTouchesOwnedPendingStamp(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows int64
		want bool
	}{
		{name: "still pending and owned", rows: 1, want: true},
		{name: "paid or superseded claim", rows: 0, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock, cleanup := newTransactionMock(t)
			defer cleanup()
			id := uuid.New()
			stamp := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
			mock.ExpectBegin()
			mock.ExpectExec(`(?s)UPDATE "transactions" SET "payment_link_sent_at"=\$1,"updated_at"=\$2 WHERE \(id = \$3 AND status = \$4 AND payment_link_sent_at = \$5\) AND "transactions"."deleted_at" IS NULL`).
				WithArgs(nil, sqlmock.AnyArg(), id, domain.TransactionStatusPending, stamp).
				WillReturnResult(sqlmock.NewResult(0, tc.rows))
			mock.ExpectCommit()
			got, err := repo.ReleasePaymentLinkEmailClaim(context.Background(), id, stamp)
			if err != nil || got != tc.want {
				t.Fatalf("release = %v, %v; want %v", got, err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReleasePaymentLinkEmailClaimReportsWriteFailure(t *testing.T) {
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "transactions" SET "payment_link_sent_at"`).WillReturnError(errors.New("db unavailable"))
	mock.ExpectRollback()
	if _, err := repo.ReleasePaymentLinkEmailClaim(context.Background(), uuid.New(), time.Now()); err == nil {
		t.Fatal("expected database error")
	}
}
