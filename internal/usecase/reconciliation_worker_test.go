package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type reconciliationClock struct{ now time.Time }

func (c reconciliationClock) Now() time.Time { return c.now }

type reconciliationRepoStub struct {
	item        *domain.PaymentReconciliation
	claimCount  int
	active      bool
	retry       bool
	lastError   string
	maxAttempts int
}

func (r *reconciliationRepoStub) Ensure(context.Context, *domain.PaymentReconciliation) error {
	return nil
}
func (r *reconciliationRepoStub) GetByTransactionID(context.Context, uuid.UUID) (*domain.PaymentReconciliation, error) {
	return r.item, nil
}
func (r *reconciliationRepoStub) ClaimDue(context.Context, uuid.UUID, time.Time, time.Duration) (*domain.PaymentReconciliation, error) {
	if r.claimCount > 0 {
		return nil, gorm.ErrRecordNotFound
	}
	r.claimCount++
	return r.item, nil
}
func (r *reconciliationRepoStub) MarkActive(context.Context, uuid.UUID, time.Time) error {
	r.active = true
	return nil
}
func (r *reconciliationRepoStub) MarkRetry(_ context.Context, _ uuid.UUID, _ time.Time, lastError string, maxAttempts int) error {
	r.retry = true
	r.lastError = lastError
	r.maxAttempts = maxAttempts
	return nil
}

type academicActivationStub struct {
	err   error
	calls int
}

func (a *academicActivationStub) ActivateEnrollment(context.Context, uuid.UUID) error {
	a.calls++
	return a.err
}

func reconciliationConfig() config.Config {
	return config.Config{PaymentReconciliationMaxAttempts: 3, PaymentReconciliationWorkerIntervalMinutes: 1}
}

func TestPaymentReconciliationWorkerMarksSuccessfulActivationActive(t *testing.T) {
	repo := &reconciliationRepoStub{item: &domain.PaymentReconciliation{ID: uuid.New(), EnrollmentID: uuid.New(), AttemptCount: 1}}
	academic := &academicActivationStub{}
	worker := NewPaymentReconciliationWorker(repo, academic, reconciliationConfig(), reconciliationClock{now: time.Unix(100, 0)})

	worker.RunOnce(context.Background())

	if academic.calls != 1 || !repo.active || repo.retry {
		t.Fatalf("calls=%d active=%v retry=%v", academic.calls, repo.active, repo.retry)
	}
}

func TestPaymentReconciliationWorkerPersistsRetryFailure(t *testing.T) {
	repo := &reconciliationRepoStub{item: &domain.PaymentReconciliation{ID: uuid.New(), EnrollmentID: uuid.New(), AttemptCount: 2}}
	academic := &academicActivationStub{err: errors.New("academic unavailable")}
	worker := NewPaymentReconciliationWorker(repo, academic, reconciliationConfig(), reconciliationClock{now: time.Unix(100, 0)})

	worker.RunOnce(context.Background())

	if academic.calls != 1 || !repo.retry || repo.lastError != "academic unavailable" || repo.maxAttempts != 3 {
		t.Fatalf("calls=%d retry=%v error=%q max=%d", academic.calls, repo.retry, repo.lastError, repo.maxAttempts)
	}
}

func TestReconciliationBackoffIsCapped(t *testing.T) {
	if got := reconciliationBackoff(1); got != 5*time.Minute {
		t.Fatalf("first backoff=%s", got)
	}
	if got := reconciliationBackoff(20); got != 6*time.Hour {
		t.Fatalf("capped backoff=%s", got)
	}
}
