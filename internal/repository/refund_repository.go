package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalidRefund = errors.New("reason and transfer_reference are required (maximum 2000 and 255 characters)")

type RefundRepository interface {
	RecordRefund(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, domain.RefundRequest) (*domain.TransactionRefund, error)
	ProcessRefund(context.Context, time.Time, func(context.Context, uuid.UUID) error) error
}

type refundRepository struct{ db *gorm.DB }

func NewRefundRepository(db *gorm.DB) RefundRepository { return &refundRepository{db: db} }

// The transaction lock is also held by activation dispatch. Refund cannot commit
// while an activation is in flight, and a later activation observes refunded.
func (r *refundRepository) RecordRefund(ctx context.Context, tenant, actor, id uuid.UUID, req domain.RefundRequest) (*domain.TransactionRefund, error) {
	req.Reason, req.TransferReference = strings.TrimSpace(req.Reason), strings.TrimSpace(req.TransferReference)
	if tenant == uuid.Nil || actor == uuid.Nil || req.Reason == "" || req.TransferReference == "" || len(req.Reason) > 2000 || len(req.TransferReference) > 255 {
		return nil, ErrInvalidRefund
	}
	var refund domain.TransactionRefund
	err := r.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var tx domain.Transaction
		if err := db.Where("id = ? AND tenant_id = ?", id, tenant).First(&tx).Error; err != nil {
			return err
		}
		if tx.SubscriptionID != nil {
			var sub domain.Subscription
			if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sub, "id = ?", *tx.SubscriptionID).Error; err != nil {
				return err
			}
		}
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ?", id, tenant).First(&tx).Error; err != nil {
			return err
		}
		if tx.Status == domain.TransactionStatusRefunded {
			return db.First(&refund, "transaction_id = ? AND tenant_id = ?", id, tenant).Error
		}
		if tx.Status != domain.TransactionStatusPaid {
			return domain.ErrInvalidTransactionStatus
		}
		if tx.SubscriptionID != nil {
			var sub domain.Subscription
			if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sub, "id = ? AND tenant_id = ?", *tx.SubscriptionID, tenant).Error; err != nil {
				return err
			}
			if err := db.Model(&sub).Update("status", "cancelled").Error; err != nil {
				return err
			}
			if err := db.Model(&SubscriptionLifecycle{}).Where("subscription_id = ?", sub.ID).Updates(map[string]interface{}{"status": "active", "next_attempt_at": nil, "last_error": nil}).Error; err != nil {
				return err
			}
			if err := db.Model(&domain.Transaction{}).Where("subscription_id = ? AND status IN ?", sub.ID, []string{"pending", "creating", "failed", "expired"}).Update("status", "cancelled").Error; err != nil {
				return err
			}
		}
		if err := db.Where("transaction_id = ? AND kind = ? AND status = ?", id, domain.ReconciliationKindActivation, domain.ReconciliationStatusPending).Delete(&domain.PaymentReconciliation{}).Error; err != nil {
			return err
		}
		if err := db.Model(&tx).Update("status", domain.TransactionStatusRefunded).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		refund = domain.TransactionRefund{TransactionID: id, TenantID: tenant, ActorID: actor, Reason: req.Reason, TransferReference: req.TransferReference, CreatedAt: now, Status: "pending", NextAttemptAt: &now}
		return db.Create(&refund).Error
	})
	return &refund, err
}

// Holding the job lock through bounded, idempotent HTTP calls gives crash recovery
// without a stale lease completing a newer attempt. A failed call leaves the refund
// intact and schedules another attempt; financial history is never rolled back.
func (r *refundRepository) ProcessRefund(ctx context.Context, now time.Time, call func(context.Context, uuid.UUID) error) error {
	return r.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var job domain.TransactionRefund
		if err := db.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", now).Order("created_at").First(&job).Error; err != nil {
			return err
		}
		var tx domain.Transaction
		if err := db.First(&tx, "id = ?", job.TransactionID).Error; err != nil {
			return err
		}
		failure := call(ctx, tx.EnrollmentID)
		updates := map[string]interface{}{"status": "completed", "attempt_count": job.AttemptCount + 1, "completed_at": now, "next_attempt_at": nil, "last_error": nil}
		if failure != nil {
			delay := time.Duration(job.AttemptCount+1) * 5 * time.Minute
			if delay > 6*time.Hour {
				delay = 6 * time.Hour
			}
			updates = map[string]interface{}{"status": "pending", "attempt_count": job.AttemptCount + 1, "next_attempt_at": now.Add(delay), "last_error": failure.Error()}
		}
		return db.Model(&job).Updates(updates).Error
	})
}

// GuardActivation serializes activation dispatch with refund recording, including
// a job already claimed before the refund arrived. Never dispatch from stale state.
func (r *paymentReconciliationRepository) GuardActivation(ctx context.Context, id uuid.UUID, call func(context.Context) error) error {
	return GetDB(ctx, r.db).Transaction(func(db *gorm.DB) error {
		var tx domain.Transaction
		if err := db.First(&tx, "id = ?", id).Error; err != nil {
			return err
		}
		if tx.SubscriptionID != nil {
			var sub domain.Subscription
			if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sub, "id = ?", *tx.SubscriptionID).Error; err != nil {
				return err
			}
			if sub.Status == "cancelled" {
				return nil
			}
		}
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&tx, "id = ?", id).Error; err != nil {
			return err
		}
		if tx.Status == domain.TransactionStatusRefunded {
			return nil
		}
		return call(context.WithValue(ctx, txKey{}, db))
	})
}
