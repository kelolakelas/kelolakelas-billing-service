package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"gorm.io/gorm"
)

type missingPeriodTransaction struct{ transactionRepoStub }

func (missingPeriodTransaction) GetBySubscriptionPeriod(context.Context, uuid.UUID, time.Time) (*domain.Transaction, error) {
	return nil, gorm.ErrRecordNotFound
}
func (missingPeriodTransaction) GetByEnrollmentID(context.Context, uuid.UUID) (*domain.Transaction, error) {
	return nil, gorm.ErrRecordNotFound
}

type lifecycleRepoStub struct {
	*renewalSubscriptionRepoStub
	suspensions int
	job         *repository.SubscriptionLifecycle
	finished    bool
}

func (s *lifecycleRepoStub) SuspendOverdue(_ context.Context, id uuid.UUID, period, now time.Time) (bool, error) {
	if s.subscription.Status != "active" {
		return false, nil
	}
	s.subscription.Status = "suspended"
	s.suspensions++
	s.job = &repository.SubscriptionLifecycle{SubscriptionID: id, EnrollmentID: s.subscription.EnrollmentID, DesiredAction: "suspend", Status: "pending"}
	return true, nil
}
func (s *lifecycleRepoStub) ResumePaid(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	s.job.DesiredAction = "resume"
	s.job.Status = "pending"
	return nil
}
func (s *lifecycleRepoStub) ClaimLifecycle(_ context.Context, now time.Time) (*repository.SubscriptionLifecycle, error) {
	if s.job == nil || s.job.Status != "pending" {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *s.job
	copy.LastAttemptAt = &now
	s.job.Status = "processing"
	return &copy, nil
}
func (s *lifecycleRepoStub) FinishLifecycle(_ context.Context, j *repository.SubscriptionLifecycle, _ time.Time, failure string, _ int) error {
	if failure != "" {
		s.job.Status = "terminal_failed"
		s.job.LastError = &failure
	} else {
		s.job.Status = "active"
		s.finished = true
	}
	return nil
}
func (s *lifecycleRepoStub) ListLifecycle(context.Context, string, int) ([]repository.SubscriptionLifecycle, error) {
	return []repository.SubscriptionLifecycle{*s.job}, nil
}

type lifecycleAcademicStub struct {
	suspends, resumes int
	resumeErr         error
}

func (a *lifecycleAcademicStub) ActivateEnrollment(context.Context, uuid.UUID) error { return nil }
func (a *lifecycleAcademicStub) ReleaseEnrollment(context.Context, uuid.UUID) error  { return nil }
func (a *lifecycleAcademicStub) SuspendEnrollment(context.Context, uuid.UUID) error {
	a.suspends++
	return nil
}
func (a *lifecycleAcademicStub) ResumeEnrollment(context.Context, uuid.UUID) error {
	a.resumes++
	return a.resumeErr
}

type emptyReconciliationStub struct {
	repository.PaymentReconciliationRepository
}

func (emptyReconciliationStub) ClaimDue(context.Context, uuid.UUID, time.Time, time.Duration) (*domain.PaymentReconciliation, error) {
	return nil, gorm.ErrRecordNotFound
}

func TestSubscriptionGraceBoundaryAndLifecycle(t *testing.T) {
	period := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	sub := &domain.Subscription{ID: uuid.New(), EnrollmentID: uuid.New(), NextBillingDate: period, Status: "active"}
	repo := &lifecycleRepoStub{renewalSubscriptionRepoStub: &renewalSubscriptionRepoStub{subscription: sub}}
	now := period.AddDate(0, 0, 7)
	worker := NewSubscriptionWorker(repo, &missingPeriodTransaction{}, nil, nil, config.Config{SubscriptionGracePeriodDays: 7}, renewalClock{now: now}).WithLifecycle(repo)
	worker.RunOnce(context.Background()) // before/at threshold, no transition (invoice failure is irrelevant)
	if repo.suspensions != 0 {
		t.Fatalf("suspended at boundary")
	}
	now = now.Add(time.Nanosecond)
	worker.clock = renewalClock{now: now}
	worker.RunOnce(context.Background())
	worker.RunOnce(context.Background())
	if repo.suspensions != 1 || sub.Status != "suspended" {
		t.Fatalf("suspends=%d status=%s", repo.suspensions, sub.Status)
	}
	academic := &lifecycleAcademicStub{}
	reconcile := NewPaymentReconciliationWorker(emptyReconciliationStub{}, academic, config.Config{}, renewalClock{now: now}).WithLifecycle(repo)
	reconcile.RunOnce(context.Background())
	reconcile.RunOnce(context.Background())
	if academic.suspends != 1 || !repo.finished {
		t.Fatalf("academic suspends=%d finished=%v", academic.suspends, repo.finished)
	}
}

func TestSubscriptionResumeConflictIsObservable(t *testing.T) {
	now := time.Now()
	sub := &domain.Subscription{ID: uuid.New(), EnrollmentID: uuid.New(), Status: "suspended"}
	repo := &lifecycleRepoStub{renewalSubscriptionRepoStub: &renewalSubscriptionRepoStub{subscription: sub}, job: &repository.SubscriptionLifecycle{SubscriptionID: sub.ID, EnrollmentID: sub.EnrollmentID, DesiredAction: "suspend", Status: "active"}}
	if err := repo.ResumePaid(context.Background(), sub.ID, sub.EnrollmentID, now); err != nil {
		t.Fatal(err)
	}
	academic := &lifecycleAcademicStub{resumeErr: errors.New("academic service returned status code 409: schedule full")}
	worker := NewPaymentReconciliationWorker(emptyReconciliationStub{}, academic, config.Config{PaymentReconciliationMaxAttempts: 1}, renewalClock{now: now}).WithLifecycle(repo)
	worker.RunOnce(context.Background())
	jobs, _ := repo.ListLifecycle(context.Background(), "", 200)
	if academic.resumes != 1 || jobs[0].Status != "terminal_failed" || jobs[0].LastError == nil {
		t.Fatalf("resume calls=%d job=%+v", academic.resumes, jobs[0])
	}
}
