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
	"github.com/kelolakelas/kelolakelas-billing-service/internal/requestid"
)

func TestReconciliationCallFailureLogsOnlySafeCorrelation(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	const secret = "provider-secret-not-for-logs"
	repo := &reconciliationRepoStub{}
	academic := &academicActivationStub{err: errors.New(secret)}
	reconciliation := &domain.PaymentReconciliation{ID: uuid.New(), EnrollmentID: uuid.New(), AttemptCount: 1}
	ctx := requestid.WithContext(context.Background(), "gateway-trace")
	if err := processClaimedReconciliation(ctx, repo, academic, reconciliationConfig(), reconciliation, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if !repo.retry || repo.lastError != secret {
		t.Fatalf("retry state was not retained")
	}
	if !strings.Contains(logs.String(), `"request_id":"gateway-trace"`) || !strings.Contains(logs.String(), "academic reconciliation call failed") {
		t.Fatalf("missing correlated failure event: %s", logs.String())
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), reconciliation.EnrollmentID.String()) {
		t.Fatal("sensitive data in reconciliation log")
	}
}
