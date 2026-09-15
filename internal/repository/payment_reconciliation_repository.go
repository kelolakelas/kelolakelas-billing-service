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

func (r *paymentReconciliationRepository) Ensure(ctx context.Context, reconciliation *domain.PaymentReconciliation) error {
	db := GetDB(ctx, r.db)
	return db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "transaction_id"}}, DoNothing: true}).Create(reconciliation).Error
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
