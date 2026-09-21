package usecase

import (
	"context"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

// reconciliationRequeueBatchSize bounds a single re-drive request so an operator
// cannot accidentally move an unbounded number of rows in one call. The endpoint
// answers with the number of rows it actually moved, so a caller that needs more
// simply repeats the request.
const reconciliationRequeueBatchSize = 200

// ReconciliationAdminUsecase exposes the reconciliation state machine to an operator
// without depending on the payment provider. Both operations are read-or-requeue only:
// nothing here changes a transaction, an enrollment, or a seat, because the whole point
// of the recovery path is to retry the durable job that already owns the side effect.
type ReconciliationAdminUsecase interface {
	ListReconciliations(ctx context.Context, status string) (*domain.ReconciliationListResponse, error)
	RequeueTerminalFailedReconciliations(ctx context.Context) (*domain.ReconciliationRequeueResponse, error)
}

type reconciliationAdminUsecase struct {
	reconciliations repository.PaymentReconciliationRepository
	clock           Clock
}

func NewReconciliationAdminUsecase(reconciliations repository.PaymentReconciliationRepository, clock ...Clock) ReconciliationAdminUsecase {
	c := Clock(realClock{})
	if len(clock) > 0 && clock[0] != nil {
		c = clock[0]
	}
	return &reconciliationAdminUsecase{reconciliations: reconciliations, clock: c}
}

// ListReconciliations returns the jobs an operator asked about. The status is validated
// against the domain set rather than passed through, so an unknown value is refused
// instead of answered with an empty page that looks like "nothing is wrong".
func (u *reconciliationAdminUsecase) ListReconciliations(ctx context.Context, status string) (*domain.ReconciliationListResponse, error) {
	if status != "" && !domain.IsReconciliationStatusValue(status) {
		return nil, domain.ErrInvalidReconciliationStatus
	}
	if u.reconciliations == nil {
		return nil, domain.ErrReconciliationUnavailable
	}
	items, err := u.reconciliations.ListByStatus(ctx, status, reconciliationRequeueBatchSize)
	if err != nil {
		return nil, err
	}
	response := &domain.ReconciliationListResponse{Items: items}
	if response.Items == nil {
		// An empty page must serialise as `[]`, not `null`, so a caller can iterate the
		// result without a nil check and tell "no failures" apart from "no field".
		response.Items = []domain.PaymentReconciliation{}
	}
	return response, nil
}

// RequeueTerminalFailedReconciliations makes permanently failed jobs eligible for the
// worker again. The repository only matches `terminal_failed` rows, so replaying the
// call cannot reset a job that is pending, in flight, or already completed — the
// acceptance criterion that requeueing an active row changes nothing is enforced by the
// statement's status guard, not by a read-then-write race in this layer.
func (u *reconciliationAdminUsecase) RequeueTerminalFailedReconciliations(ctx context.Context) (*domain.ReconciliationRequeueResponse, error) {
	if u.reconciliations == nil {
		return nil, domain.ErrReconciliationUnavailable
	}
	requeued, err := u.reconciliations.RequeueTerminalFailed(ctx, u.clock.Now(), reconciliationRequeueBatchSize)
	if err != nil {
		return nil, err
	}
	return &domain.ReconciliationRequeueResponse{Requeued: requeued, RequeuedAt: u.clock.Now()}, nil
}
