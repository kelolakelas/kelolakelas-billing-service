package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type paymentReconciliationRepository struct{ db *gorm.DB }

func NewPaymentReconciliationRepository(db *gorm.DB) PaymentReconciliationRepository {
	return &paymentReconciliationRepository{db: db}
}

// EnsureActivation records that a paid transaction still owes Academic an
// activation. A row that already completed an activation is left untouched, while
// a row that was turned into a release job is converted back: the seat may already
// be gone, so the attempt must stay visible instead of being swallowed.
func (r *paymentReconciliationRepository) EnsureActivation(ctx context.Context, reconciliation *domain.PaymentReconciliation) error {
	db := GetDB(ctx, r.db)
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "transaction_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"kind":            reconciliation.Kind,
			"status":          reconciliation.Status,
			"next_attempt_at": reconciliation.NextAttemptAt,
			"last_error":      nil,
		}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Or(
				clause.Neq{Column: clause.Column{Table: "payment_reconciliations", Name: "kind"}, Value: domain.ReconciliationKindActivation},
				clause.Neq{Column: clause.Column{Table: "payment_reconciliations", Name: "status"}, Value: domain.ReconciliationStatusActive},
			),
		}},
	}).Create(reconciliation).Error
}

// EnqueueRelease records that a failed or expired transaction must release its
// Academic seat. Rows that already owe Academic something — including a completed
// activation — are never overwritten here, because a failure notification must not
// revoke a seat the activation just confirmed. Expiry calls this only after the row
// is already `expired`, which is precisely the state that owes a release.
func (r *paymentReconciliationRepository) EnqueueRelease(ctx context.Context, reconciliation *domain.PaymentReconciliation) error {
	db := GetDB(ctx, r.db)
	return db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "transaction_id"}}, DoNothing: true}).Create(reconciliation).Error
}

// CancelPendingRelease withdraws a release job that has not been claimed yet, which
// the re-invoice path uses so a replacement invoice cannot race the worker into
// dropping a seat the parent is about to pay for. The row is deleted rather than
// re-kinded: a withdrawn job owes Academic nothing, and re-using the row as an
// activation would let the worker confirm a seat for an invoice that is still
// unpaid. Only `pending` rows are affected — a job already in flight or already
// accepted describes a seat that is genuinely released, and that evidence is kept so
// a later paid callback surfaces the rejection instead of hiding it.
func (r *paymentReconciliationRepository) CancelPendingRelease(ctx context.Context, transactionID uuid.UUID) error {
	return GetDB(ctx, r.db).
		Where("transaction_id = ? AND kind = ? AND status = ?", transactionID, domain.ReconciliationKindRelease, domain.ReconciliationStatusPending).
		Delete(&domain.PaymentReconciliation{}).Error
}

func (r *paymentReconciliationRepository) GetByTransactionID(ctx context.Context, transactionID uuid.UUID) (*domain.PaymentReconciliation, error) {
	var reconciliation domain.PaymentReconciliation
	err := GetDB(ctx, r.db).Where("transaction_id = ?", transactionID).First(&reconciliation).Error
	if err != nil {
		return nil, err
	}
	return &reconciliation, nil
}

func (r *paymentReconciliationRepository) ClaimDue(ctx context.Context, transactionID uuid.UUID, now time.Time, lease time.Duration) (*domain.PaymentReconciliation, error) {
	var reconciliation domain.PaymentReconciliation
	leaseCutoff := now.Add(-lease)
	err := GetDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		query := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		if transactionID != uuid.Nil {
			query = query.Where("transaction_id = ?", transactionID)
		}
		// The paid path claims by transaction id and must only ever dispatch the
		// activation it is paying for; release jobs are drained by the worker's
		// kind-agnostic sweep.
		if transactionID != uuid.Nil {
			query = query.Where("kind = ?", domain.ReconciliationKindActivation)
		}
		err := query.
			Where("(status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)) OR (status = ? AND last_attempt_at < ?)",
				domain.ReconciliationStatusPending, now, domain.ReconciliationStatusProcessing, leaseCutoff).
			Order("created_at ASC").First(&reconciliation).Error
		if err != nil {
			return err
		}
		return tx.Model(&domain.PaymentReconciliation{}).Where("id = ?", reconciliation.ID).Updates(map[string]interface{}{
			"status":          domain.ReconciliationStatusProcessing,
			"attempt_count":   gorm.Expr("attempt_count + 1"),
			"last_attempt_at": now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	reconciliation.Status = domain.ReconciliationStatusProcessing
	reconciliation.AttemptCount++
	reconciliation.LastAttemptAt = &now
	return &reconciliation, nil
}

func (r *paymentReconciliationRepository) MarkActive(ctx context.Context, id uuid.UUID, completedAt time.Time) error {
	return GetDB(ctx, r.db).Model(&domain.PaymentReconciliation{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":          domain.ReconciliationStatusActive,
		"completed_at":    completedAt,
		"next_attempt_at": nil,
		"last_error":      nil,
	}).Error
}

func (r *paymentReconciliationRepository) MarkRetry(ctx context.Context, id uuid.UUID, nextAttemptAt time.Time, lastError string, maxAttempts int) error {
	status := domain.ReconciliationStatusPending
	if maxAttempts > 0 {
		var current domain.PaymentReconciliation
		if err := GetDB(ctx, r.db).First(&current, "id = ?", id).Error; err != nil {
			return err
		}
		if current.AttemptCount >= maxAttempts {
			status = domain.ReconciliationStatusTerminalFailed
			nextAttemptAt = time.Time{}
		}
	}
	var nextAttempt interface{} = nextAttemptAt
	if nextAttemptAt.IsZero() {
		nextAttempt = nil
	}
	return GetDB(ctx, r.db).Model(&domain.PaymentReconciliation{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":          status,
		"next_attempt_at": nextAttempt,
		"last_error":      lastError,
	}).Error
}
