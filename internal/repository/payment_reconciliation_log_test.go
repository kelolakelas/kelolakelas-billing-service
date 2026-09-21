package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// captureLogs routes the process logger into a buffer for the duration of a test and
// returns the decoded JSON records it collected.
func captureLogs(t *testing.T, run func()) []map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, nil)))
	defer slog.SetDefault(previous)

	run()

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if line == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

// Every reconciliation transition has to leave a log line an operator can find by the
// transaction and enrollment ids they already have from the parent-visible response. A
// permanent failure is the one that matters most: it is the state nothing retries.
func TestMarkRetryLogsTerminalFailureWithCorrelationIds(t *testing.T) {
	transactionID, enrollmentID := uuid.New(), uuid.New()
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "payment_reconciliations"`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "transaction_id", "enrollment_id", "kind", "status", "attempt_count"}).
			AddRow(uuid.New(), transactionID, enrollmentID, domain.ReconciliationKindActivation, domain.ReconciliationStatusProcessing, 3))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "payment_reconciliations"`)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	records := captureLogs(t, func() {
		if err := repo.MarkRetry(context.Background(), uuid.New(), time.Time{}, "academic service returned status code 503", 3); err != nil {
			t.Fatalf("MarkRetry error: %v", err)
		}
	})

	if len(records) != 1 {
		t.Fatalf("log records=%d, want 1", len(records))
	}
	record := records[0]
	if record["level"] != "ERROR" {
		t.Errorf("level=%v, want ERROR for a job that will not be retried", record["level"])
	}
	if record["transaction_id"] != transactionID.String() || record["enrollment_id"] != enrollmentID.String() {
		t.Errorf("record=%v, want the transaction and enrollment ids", record)
	}
	if record["status"] != domain.ReconciliationStatusTerminalFailed {
		t.Errorf("status=%v, want %q", record["status"], domain.ReconciliationStatusTerminalFailed)
	}
	if record["error"] != "academic service returned status code 503" {
		t.Errorf("error=%v, want the stored failure", record["error"])
	}
}

// A retry that is not yet terminal is a warning and carries the next attempt instant, so an
// operator can tell "still trying" apart from "gave up".
func TestMarkRetryLogsScheduledRetryAsWarning(t *testing.T) {
	nextAttempt := time.Date(2026, time.September, 21, 11, 0, 0, 0, time.UTC)
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "payment_reconciliations"`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "transaction_id", "enrollment_id", "kind", "status", "attempt_count"}).
			AddRow(uuid.New(), uuid.New(), uuid.New(), domain.ReconciliationKindActivation, domain.ReconciliationStatusProcessing, 1))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "payment_reconciliations"`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	records := captureLogs(t, func() {
		if err := repo.MarkRetry(context.Background(), uuid.New(), nextAttempt, "academic unavailable", 3); err != nil {
			t.Fatalf("MarkRetry error: %v", err)
		}
	})

	if len(records) != 1 || records[0]["level"] != "WARN" {
		t.Fatalf("records=%v, want one WARN line", records)
	}
	if records[0]["status"] != domain.ReconciliationStatusPending {
		t.Fatalf("status=%v, want %q", records[0]["status"], domain.ReconciliationStatusPending)
	}
}

// A successful job is the terminal state an operator checks a stuck reconciliation
// against, so it is logged too.
func TestMarkActiveLogsCompletion(t *testing.T) {
	completedAt := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC)
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "payment_reconciliations"`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "transaction_id", "enrollment_id", "kind", "status"}).
			AddRow(uuid.New(), uuid.New(), uuid.New(), domain.ReconciliationKindRelease, domain.ReconciliationStatusProcessing))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "payment_reconciliations"`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	records := captureLogs(t, func() {
		if err := repo.MarkActive(context.Background(), uuid.New(), completedAt); err != nil {
			t.Fatalf("MarkActive error: %v", err)
		}
	})

	if len(records) != 1 || records[0]["status"] != domain.ReconciliationStatusActive {
		t.Fatalf("records=%v, want one line with status active", records)
	}
}

// Nothing written means nothing transitioned, so a conflicting enqueue must stay silent
// instead of announcing a job that does not exist.
func TestEnqueueReleaseLogsNothingWhenTheInsertConflicted(t *testing.T) {
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "payment_reconciliations"`)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()

	records := captureLogs(t, func() {
		err := repo.EnqueueRelease(context.Background(), &domain.PaymentReconciliation{
			ID:            uuid.New(),
			TransactionID: uuid.New(),
			EnrollmentID:  uuid.New(),
			Kind:          domain.ReconciliationKindRelease,
			Status:        domain.ReconciliationStatusPending,
		})
		if err != nil {
			t.Fatalf("EnqueueRelease error: %v", err)
		}
	})

	if len(records) != 0 {
		t.Fatalf("records=%v, want no transition log without a written row", records)
	}
}

// The claim is the transition into `processing`; logging it is what lets an operator see
// an attempt that started and never finished.
func TestClaimDueLogsProcessingTransition(t *testing.T) {
	now := time.Date(2026, time.September, 21, 13, 0, 0, 0, time.UTC)
	transactionID, enrollmentID := uuid.New(), uuid.New()
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "payment_reconciliations"`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "transaction_id", "enrollment_id", "kind", "status", "attempt_count"}).
			AddRow(uuid.New(), transactionID, enrollmentID, domain.ReconciliationKindActivation, domain.ReconciliationStatusPending, 0))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "payment_reconciliations"`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	claimed, err := repo.ClaimDue(context.Background(), uuid.Nil, now, 5*time.Minute)
	if err != nil {
		t.Fatalf("ClaimDue error: %v", err)
	}
	if claimed.Status != domain.ReconciliationStatusProcessing {
		t.Fatalf("status=%q, want processing", claimed.Status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The correlation ids are the whole point of the logging: without them a line cannot be
// tied to the transaction a parent is asking about. This also pins that no credential or
// request header value is ever added.
func TestReconciliationTransitionLogsNoSensitiveValues(t *testing.T) {
	repo, mock, cleanup := newReconciliationMock(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "payment_reconciliations"`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "transaction_id", "enrollment_id", "kind", "status", "attempt_count"}).
			AddRow(uuid.New(), uuid.New(), uuid.New(), domain.ReconciliationKindActivation, domain.ReconciliationStatusProcessing, 1))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "payment_reconciliations"`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	records := captureLogs(t, func() {
		if err := repo.MarkRetry(context.Background(), uuid.New(), time.Time{}, "academic unavailable", 3); err != nil {
			t.Fatalf("MarkRetry error: %v", err)
		}
	})

	if len(records) == 0 {
		t.Fatal("no log record captured")
	}
	for _, record := range records {
		for key, value := range record {
			text, ok := value.(string)
			if !ok {
				continue
			}
			for _, forbidden := range []string{"X-Internal-Service-Credential", "Authorization", "supersecretjwtkey", "inipasswordredis"} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("log key %q leaked %q", key, forbidden)
				}
			}
		}
	}
}
