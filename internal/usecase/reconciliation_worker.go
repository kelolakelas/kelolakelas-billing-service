package usecase

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/academic"
)

const reconciliationLease = 5 * time.Minute

type PaymentReconciliationWorker struct {
	reconciliations repository.PaymentReconciliationRepository
	academic        academic.Client
	cfg             config.Config
	clock           Clock
}

func NewPaymentReconciliationWorker(reconciliations repository.PaymentReconciliationRepository, academicClient academic.Client, cfg config.Config, clock ...Clock) *PaymentReconciliationWorker {
	c := Clock(realClock{})
	if len(clock) > 0 && clock[0] != nil {
		c = clock[0]
	}
	return &PaymentReconciliationWorker{reconciliations: reconciliations, academic: academicClient, cfg: cfg, clock: c}
}

func (w *PaymentReconciliationWorker) Run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	interval := time.Duration(w.cfg.PaymentReconciliationWorkerIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = time.Minute
	}
	w.RunOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

func (w *PaymentReconciliationWorker) RunOnce(ctx context.Context) {
	for ctx.Err() == nil {
		reconciliation, err := w.reconciliations.ClaimDue(ctx, uuid.Nil, w.clock.Now(), reconciliationLease)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return
		}
		if err != nil {
			return
		}
		if err := w.attempt(ctx, reconciliation); err != nil {
			// The state is persisted by attempt; continue processing other due rows.
			continue
		}
	}
}

func (w *PaymentReconciliationWorker) attempt(ctx context.Context, reconciliation *domain.PaymentReconciliation) error {
	return processClaimedReconciliation(ctx, w.reconciliations, w.academic, w.cfg, reconciliation, w.clock.Now())
}

// processClaimedReconciliation performs the Academic call a claimed job asks for.
// An activation confirms a seat the parent paid for; a release gives the seat back
// after a failed or expired payment. Both share the same durable retry loop, so a
// transient Academic outage is retried with backoff and a permanent rejection is
// eventually recorded as terminal_failed with the provider error in last_error.
// An activation that Academic rejects with 409 (the seat is already dropped or held
// by another payment) is not a transient error, but it is still recorded rather than
// swallowed: the row keeps the rejection in last_error until it reaches the attempt
// limit, which is the operator-visible signal that a paid enrollment did not activate.
func processClaimedReconciliation(ctx context.Context, repo repository.PaymentReconciliationRepository, academicClient academic.Client, cfg config.Config, reconciliation *domain.PaymentReconciliation, now time.Time) error {
	if academicClient == nil {
		return repo.MarkRetry(ctx, reconciliation.ID, now.Add(reconciliationBackoff(reconciliation.AttemptCount)), "academic client is unavailable", cfg.PaymentReconciliationMaxAttempts)
	}
	var err error
	switch reconciliation.Kind {
	case domain.ReconciliationKindRelease:
		err = academicClient.ReleaseEnrollment(ctx, reconciliation.EnrollmentID)
	default:
		err = academicClient.ActivateEnrollment(ctx, reconciliation.EnrollmentID)
	}
	if err != nil {
		return repo.MarkRetry(ctx, reconciliation.ID, now.Add(reconciliationBackoff(reconciliation.AttemptCount)), err.Error(), cfg.PaymentReconciliationMaxAttempts)
	}
	return repo.MarkActive(ctx, reconciliation.ID, now)
}

func reconciliationBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	minutes := math.Min(360, 5*math.Pow(2, float64(attempt-1)))
	return time.Duration(minutes) * time.Minute
}
