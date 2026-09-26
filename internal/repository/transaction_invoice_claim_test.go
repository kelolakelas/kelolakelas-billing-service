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

// claimTimeout is the value the tests feed to the claim statements. A ten minute window
// keeps the cutoff far away from the timestamps used below, so an accidental off-by-one in
// the staleness comparison shows up as a failed expectation instead of passing by luck.
const claimTimeout = 10

// ClaimInvoice is the exclusivity primitive for the first invoice of an enrollment: the
// predicate is the only thing that stops two parallel requests from both calling the
// provider, so the statement itself is asserted rather than just its arguments. A row that
// already carries a payment link must never be claimed again, and a `creating` row may only
// be taken over once its claim has gone stale.
func TestClaimInvoiceTakesTheClaimOnlyForClaimableRows(t *testing.T) {
	now := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	staleBefore := now.Add(-claimTimeout * time.Minute)
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE "transactions" SET .*WHERE \(id = \$5 AND checkout_session_url IS NULL AND .*status IN \(\$6,\$7\).*status = \$8 AND \(invoice_claimed_at IS NULL OR invoice_claimed_at <= \$9\)`).
		// The write stamps the claim and clears any reason left by a previous failure, so a
		// reclaimed row reports the attempt that is actually running.
		WithArgs(now, nil, domain.TransactionStatusCreating, now, id,
			domain.TransactionStatusPending, domain.TransactionStatusFailed, domain.TransactionStatusCreating, staleBefore).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	claimed, err := repo.ClaimInvoice(context.Background(), id, now, claimTimeout)
	if err != nil {
		t.Fatalf("ClaimInvoice error: %v", err)
	}
	if !claimed {
		t.Fatal("ClaimInvoice reported false although the claim update matched a row")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A claim that loses the race must report false without an error, because the caller has to
// fall back to the invoice another request is creating rather than failing the payment.
func TestClaimOutcomeEmailUsesOnlyPaidOrFailedStatuses(t *testing.T) {
	now := time.Date(2026, time.September, 27, 10, 0, 0, 0, time.UTC)
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE "transactions" SET "paid_email_sent_at"=\$1,"updated_at"=\$2 WHERE \(id = \$3 AND status = \$4 AND paid_email_sent_at IS NULL\) AND "transactions"."deleted_at" IS NULL`).
		WithArgs(now, sqlmock.AnyArg(), id, domain.TransactionStatusPaid).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	claimed, err := repo.ClaimOutcomeEmail(context.Background(), id, domain.TransactionStatusPaid, now)
	if err != nil || !claimed {
		t.Fatalf("ClaimOutcomeEmail(paid) = %v, %v; want true, nil", claimed, err)
	}

	if claimed, err := repo.ClaimOutcomeEmail(context.Background(), id, "pending", now); err != nil || claimed {
		t.Fatalf("ClaimOutcomeEmail(pending) = %v, %v; want false, nil", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestClaimInvoiceReportsFalseWhenAnotherRequestOwnsTheRow(t *testing.T) {
	now := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE "transactions" SET .*WHERE \(id = \$5 AND checkout_session_url IS NULL`).
		WithArgs(now, nil, domain.TransactionStatusCreating, now, id,
			domain.TransactionStatusPending, domain.TransactionStatusFailed, domain.TransactionStatusCreating,
			now.Add(-claimTimeout*time.Minute)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	claimed, err := repo.ClaimInvoice(context.Background(), id, now, claimTimeout)
	if err != nil {
		t.Fatalf("ClaimInvoice error: %v", err)
	}
	if claimed {
		t.Fatal("ClaimInvoice reported true although no row matched")
	}
}

// A non-positive timeout would silently disable recovery by making every claim look stale,
// so the repository must fall back to the documented default instead of trusting the value.
func TestClaimInvoiceFallsBackToTheDefaultTimeout(t *testing.T) {
	now := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE "transactions" SET .*WHERE \(id = \$5 AND checkout_session_url IS NULL`).
		WithArgs(now, nil, domain.TransactionStatusCreating, now, id,
			domain.TransactionStatusPending, domain.TransactionStatusFailed, domain.TransactionStatusCreating,
			now.Add(-domain.DefaultTransactionClaimTimeoutMinutes*time.Minute)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if _, err := repo.ClaimInvoice(context.Background(), id, now, 0); err != nil {
		t.Fatalf("ClaimInvoice error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// ClaimReinvoice follows ClaimInvoice, so the abandoned `creating` row must also be
// reclaimable here: reissuing an expired invoice for an enrollment whose first attempt died
// mid-flight otherwise parks the enrollment for good.
func TestClaimReinvoiceIncludesTheAbandonedCreatingRow(t *testing.T) {
	now := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	staleBefore := now.Add(-claimTimeout * time.Minute)
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE "transactions" SET .*WHERE \(id = \$7 AND \( status = \$8 OR .*status IN \(\$9,\$10\).*invoice_expires_at IS NOT NULL AND invoice_expires_at <= \$11.*status = \$12 AND checkout_session_url IS NULL AND \(invoice_claimed_at IS NULL OR invoice_claimed_at <= \$13\)`).
		// The reissue drops the previous link and intent, then takes the claim under the same
		// timestamp discipline as the first invoice.
		WithArgs(nil, now, nil, nil, domain.TransactionStatusCreating, now, id,
			domain.TransactionStatusExpired,
			domain.TransactionStatusPending, domain.TransactionStatusFailed, now,
			domain.TransactionStatusCreating, staleBefore).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	claimed, err := repo.ClaimReinvoice(context.Background(), id, now, claimTimeout)
	if err != nil {
		t.Fatalf("ClaimReinvoice error: %v", err)
	}
	if !claimed {
		t.Fatal("ClaimReinvoice reported false although the claim update matched a row")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// RestoreFailedInvoiceClaim is the recovery path for a provider failure. It may only touch
// the row the caller still owns: a claim that was taken over in the meantime, or a row that
// already received a link, must be left alone so a late failure cannot erase a good invoice.
func TestRestoreFailedInvoiceClaimOnlyRewritesTheOwnedClaim(t *testing.T) {
	now := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE "transactions" SET .*WHERE \(id = \$4 AND status = \$5 AND checkout_session_url IS NULL\)`).
		WithArgs("duitku unavailable", domain.TransactionStatusFailed, now, id, domain.TransactionStatusCreating).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	restored, err := repo.RestoreFailedInvoiceClaim(context.Background(), id, "duitku unavailable", now)
	if err != nil {
		t.Fatalf("RestoreFailedInvoiceClaim error: %v", err)
	}
	if !restored {
		t.Fatal("RestoreFailedInvoiceClaim reported false although the release matched a row")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The release reports false when the conditional update matched nothing, which is how the
// caller learns that the row moved on and the claim timeout has to finish the recovery.
func TestRestoreFailedInvoiceClaimReportsFalseWhenNoRowMatched(t *testing.T) {
	now := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE "transactions" SET .*WHERE \(id = \$4 AND status = \$5 AND checkout_session_url IS NULL\)`).
		WithArgs("duitku unavailable", domain.TransactionStatusFailed, now, id, domain.TransactionStatusCreating).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	restored, err := repo.RestoreFailedInvoiceClaim(context.Background(), id, "duitku unavailable", now)
	if err != nil {
		t.Fatalf("RestoreFailedInvoiceClaim error: %v", err)
	}
	if restored {
		t.Fatal("RestoreFailedInvoiceClaim reported true although no row matched")
	}
}

// A database error must surface rather than be read as "the row moved on", because the
// caller logs the outcome and a silent failure would hide a broken recovery path.
func TestRestoreFailedInvoiceClaimPropagatesRepositoryErrors(t *testing.T) {
	now := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	id := uuid.New()
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE "transactions" SET .*WHERE \(id = \$4 AND status = \$5 AND checkout_session_url IS NULL\)`).
		WithArgs("duitku unavailable", domain.TransactionStatusFailed, now, id, domain.TransactionStatusCreating).
		WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()

	restored, err := repo.RestoreFailedInvoiceClaim(context.Background(), id, "duitku unavailable", now)
	if err == nil {
		t.Fatal("RestoreFailedInvoiceClaim error = nil, want the repository failure")
	}
	if restored {
		t.Fatal("RestoreFailedInvoiceClaim reported true together with an error")
	}
}
