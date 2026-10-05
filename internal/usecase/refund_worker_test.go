package usecase

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"gorm.io/gorm"
	"testing"
	"time"
)

type refundJobStub struct {
	id              uuid.UUID
	done, attempted bool
	failure         error
}

func (*refundJobStub) RecordRefund(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, domain.RefundRequest) (*domain.TransactionRefund, error) {
	return nil, nil
}
func (r *refundJobStub) ProcessRefund(ctx context.Context, _ time.Time, call func(context.Context, uuid.UUID) error) error {
	if r.done || r.attempted {
		return gorm.ErrRecordNotFound
	}
	r.attempted = true
	r.failure = call(ctx, r.id)
	r.done = r.failure == nil
	return nil
}

type refundAcademicStub struct {
	academicActivationStub
	id    uuid.UUID
	calls int
	err   error
}

func (a *refundAcademicStub) EndRefundedEnrollment(_ context.Context, id uuid.UUID) error {
	a.calls++
	a.id = id
	return a.err
}
func TestRefundWorkerRetryAndIdempotency(t *testing.T) {
	ctx := context.Background()
	repo := &refundJobStub{id: uuid.New()}
	client := &refundAcademicStub{err: errors.New("academic unavailable")}
	recon := &reconciliationRepoStub{claimCount: 1}
	worker := NewPaymentReconciliationWorker(recon, client, reconciliationConfig()).WithRefunds(repo)
	worker.RunOnce(ctx)
	if repo.done || repo.failure == nil || client.id != repo.id {
		t.Fatal("failure lost or wrong enrollment")
	}
	repo.attempted = false
	client.err = nil
	worker.RunOnce(ctx)
	worker.RunOnce(ctx)
	if !repo.done || client.calls != 2 {
		t.Fatalf("done=%v calls=%d", repo.done, client.calls)
	}
}
