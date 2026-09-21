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
	item         *domain.PaymentReconciliation
	claimCount   int
	active       bool
	retry        bool
	ensureCount  int
	releaseCount int
	cancelCount  int
	lastError    string
	maxAttempts  int
}

func (r *reconciliationRepoStub) EnsureActivation(context.Context, *domain.PaymentReconciliation) error {
	r.ensureCount++
	return nil
}
func (r *reconciliationRepoStub) EnqueueRelease(_ context.Context, reconciliation *domain.PaymentReconciliation) error {
	r.releaseCount++
	r.item = reconciliation
	return nil
}
func (r *reconciliationRepoStub) CancelPendingRelease(context.Context, uuid.UUID) error {
	r.cancelCount++
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

// ListByStatus and RequeueTerminalFailed exist so the stub keeps satisfying the
// repository interface the worker shares with the operator endpoint. They are unused by
// these tests; the reconciliation admin usecase has its own stub.
func (r *reconciliationRepoStub) ListByStatus(context.Context, string, int) ([]domain.PaymentReconciliation, error) {
	return nil, nil
}

func (r *reconciliationRepoStub) RequeueTerminalFailed(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

type academicActivationStub struct {
	err          error
	releaseErr   error
	calls        int
	releaseCalls int
}

func (a *academicActivationStub) ActivateEnrollment(context.Context, uuid.UUID) error {
	a.calls++
	return a.err
}

func (a *academicActivationStub) ReleaseEnrollment(context.Context, uuid.UUID) error {
	a.releaseCalls++
	return a.releaseErr
}

func (a *academicActivationStub) callsSet() bool { return a.calls > 0 }

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

// A release job must reach Academic's release endpoint and must never activate: the
// whole point of the job is that the parent did not pay, so confirming the seat would
// grant access for free.
func TestPaymentReconciliationWorkerReleasesSeatForReleaseJob(t *testing.T) {
	repo := &reconciliationRepoStub{item: &domain.PaymentReconciliation{
		ID:           uuid.New(),
		EnrollmentID: uuid.New(),
		Kind:         domain.ReconciliationKindRelease,
		AttemptCount: 1,
	}}
	academic := &academicActivationStub{}
	worker := NewPaymentReconciliationWorker(repo, academic, reconciliationConfig(), reconciliationClock{now: time.Unix(100, 0)})

	worker.RunOnce(context.Background())

	if academic.releaseCalls != 1 {
		t.Fatalf("release calls=%d, want 1", academic.releaseCalls)
	}
	if academic.calls != 0 {
		t.Fatalf("activate calls=%d, want 0 for a release job", academic.calls)
	}
	if !repo.active || repo.retry {
		t.Fatalf("active=%v retry=%v, want a completed release", repo.active, repo.retry)
	}
}

// Activation jobs keep their original behaviour, so the release support cannot
// silently take over the paid path.
func TestPaymentReconciliationWorkerActivatesForActivationJob(t *testing.T) {
	repo := &reconciliationRepoStub{item: &domain.PaymentReconciliation{
		ID:           uuid.New(),
		EnrollmentID: uuid.New(),
		Kind:         domain.ReconciliationKindActivation,
		AttemptCount: 1,
	}}
	academic := &academicActivationStub{}
	worker := NewPaymentReconciliationWorker(repo, academic, reconciliationConfig(), reconciliationClock{now: time.Unix(100, 0)})

	worker.RunOnce(context.Background())

	if academic.calls != 1 || academic.releaseCalls != 0 {
		t.Fatalf("activate=%d release=%d, want 1 and 0", academic.calls, academic.releaseCalls)
	}
	if !repo.active {
		t.Fatalf("active=%v, want true", repo.active)
	}
}

// A rejected release is retried with the provider error preserved, so a seat that
// could not be freed is visible to operators instead of being dropped silently.
func TestPaymentReconciliationWorkerRetriesFailedRelease(t *testing.T) {
	repo := &reconciliationRepoStub{item: &domain.PaymentReconciliation{
		ID:           uuid.New(),
		EnrollmentID: uuid.New(),
		Kind:         domain.ReconciliationKindRelease,
		AttemptCount: 2,
	}}
	academic := &academicActivationStub{releaseErr: errors.New("academic unavailable")}
	worker := NewPaymentReconciliationWorker(repo, academic, reconciliationConfig(), reconciliationClock{now: time.Unix(100, 0)})

	worker.RunOnce(context.Background())

	if academic.releaseCalls != 1 {
		t.Fatalf("release calls=%d, want 1", academic.releaseCalls)
	}
	if !repo.retry || repo.active {
		t.Fatalf("retry=%v active=%v, want a recorded retry", repo.retry, repo.active)
	}
	if repo.lastError != "academic unavailable" {
		t.Fatalf("lastError=%q, want the provider error", repo.lastError)
	}
}

// An activation Academic rejects — the acceptance criterion's 409 for an enrollment
// that was already dropped — must be recorded rather than swallowed, so a paid seat
// that never activated stays observable until the attempt limit is reached.
func TestPaymentReconciliationWorkerRecordsRejectedActivation(t *testing.T) {
	repo := &reconciliationRepoStub{item: &domain.PaymentReconciliation{
		ID:           uuid.New(),
		EnrollmentID: uuid.New(),
		Kind:         domain.ReconciliationKindActivation,
		AttemptCount: 3,
	}}
	academic := &academicActivationStub{err: errors.New("academic service returned status code 409: enrollment is not pending")}
	worker := NewPaymentReconciliationWorker(repo, academic, reconciliationConfig(), reconciliationClock{now: time.Unix(100, 0)})

	worker.RunOnce(context.Background())

	if repo.active {
		t.Fatalf("active=true, want the rejection to be recorded instead of confirmed")
	}
	if !repo.retry || repo.lastError == "" {
		t.Fatalf("retry=%v lastError=%q, want the rejection persisted", repo.retry, repo.lastError)
	}
}
