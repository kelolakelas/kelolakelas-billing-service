package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type transactionRepository struct {
	db *gorm.DB
}

func NewTransactionRepository(db *gorm.DB) TransactionRepository {
	return &transactionRepository{db: db}
}

func (r *transactionRepository) Create(ctx context.Context, transaction *domain.Transaction) error {
	return r.getDB(ctx).Create(transaction).Error
}

func (r *transactionRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := r.getDB(ctx).Preload("Reconciliation").First(&tx, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &tx, nil
}

func (r *transactionRepository) GetByMerchantOrderID(ctx context.Context, merchantOrderID string) (*domain.Transaction, error) {
	var tx domain.Transaction
	err := r.getDB(ctx).Preload("Reconciliation").First(&tx, "merchant_order_id = ?", merchantOrderID).Error
	return &tx, err
}

func (r *transactionRepository) GetByEnrollmentID(ctx context.Context, enrollmentID uuid.UUID) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := r.getDB(ctx).Where("enrollment_id = ?", enrollmentID).Order("created_at DESC").First(&tx).Error; err != nil {
		return nil, err
	}
	return &tx, nil
}

func (r *transactionRepository) GetBySubscriptionPeriod(ctx context.Context, subscriptionID uuid.UUID, period time.Time) (*domain.Transaction, error) {
	var tx domain.Transaction
	err := r.getDB(ctx).Where("subscription_id = ? AND billing_period_start = ?", subscriptionID, period).First(&tx).Error
	return &tx, err
}

func (r *transactionRepository) GetByPaymentIntentID(ctx context.Context, paymentIntentID string) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := r.getDB(ctx).First(&tx, "payment_intent_id = ?", paymentIntentID).Error; err != nil {
		return nil, err
	}
	return &tx, nil
}

func (r *transactionRepository) List(ctx context.Context, tenantID, parentID *uuid.UUID, query domain.TransactionQuery) ([]domain.Transaction, int64, error) {
	db := r.db.WithContext(ctx).Model(&domain.Transaction{}).Preload("Reconciliation")
	if tenantID != nil {
		db = db.Where("tenant_id = ?", *tenantID)
	}
	if parentID != nil {
		db = db.Where("parent_id = ?", *parentID)
	}
	if query.Status != "" {
		db = db.Where("status = ?", query.Status)
	}
	if query.StudentID != nil {
		db = db.Where("student_id = ?", *query.StudentID)
	}
	if query.EnrollmentID != nil {
		db = db.Where("enrollment_id = ?", *query.EnrollmentID)
	}
	if query.Search != "" {
		db = db.Where("merchant_order_id ILIKE ? OR payment_intent_id ILIKE ?", "%"+query.Search+"%", "%"+query.Search+"%")
	}
	if query.DateFrom != nil {
		db = db.Where("created_at >= ?", *query.DateFrom)
	}
	if query.DateTo != nil {
		db = db.Where("created_at <= ?", *query.DateTo)
	}
	var total int64
	if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []domain.Transaction
	err := db.Order("created_at DESC").Limit(query.PageSize).Offset((query.Page - 1) * query.PageSize).Find(&items).Error
	return items, total, err
}

func (r *transactionRepository) Update(ctx context.Context, transaction *domain.Transaction) error {
	return r.getDB(ctx).Save(transaction).Error
}

func (r *transactionRepository) getDB(ctx context.Context) *gorm.DB { return GetDB(ctx, r.db) }

func (r *transactionRepository) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := r.getDB(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&tx, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &tx, nil
}

func (r *transactionRepository) ClaimInvoice(ctx context.Context, id uuid.UUID) (bool, error) {
	result := r.getDB(ctx).Model(&domain.Transaction{}).
		Where("id = ? AND status IN ? AND checkout_session_url IS NULL", id, []string{"pending", "failed"}).
		Updates(map[string]interface{}{"status": "creating"})
	return result.RowsAffected == 1, result.Error
}

// ClaimReinvoice takes ownership of issuing a replacement invoice for a transaction
// whose invoice can no longer be paid. Only `expired` transactions, unpaid
// transactions that never received a payment link, and unpaid transactions whose
// stored expiry has already passed can be claimed; a transaction that has been
// paid, or whose payment link is still valid, never matches. The previous payment
// link and payment intent are cleared so the stale invoice is never served again,
// and the stored expiry stays in place until the replacement invoice is created.
func (r *transactionRepository) ClaimReinvoice(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	result := r.getDB(ctx).Model(&domain.Transaction{}).
		Where(`id = ? AND (
			status = ? OR (
				status IN ? AND (
					checkout_session_url IS NULL OR
					(invoice_expires_at IS NOT NULL AND invoice_expires_at <= ?)
				)
			)
		)`, id, domain.TransactionStatusExpired, []string{domain.TransactionStatusPending, domain.TransactionStatusFailed}, now).
		Updates(map[string]interface{}{
			"status":               "creating",
			"checkout_session_url": nil,
			"payment_intent_id":    nil,
		})
	return result.RowsAffected == 1, result.Error
}

// ExpireDue moves due unpaid transactions to the terminal `expired` status in a
// single conditional update. The inner SELECT uses FOR UPDATE SKIP LOCKED so
// concurrent replicas claim disjoint rows, and the outer WHERE re-checks the
// status so an invoice paid in the meantime is never expired.
func (r *transactionRepository) ExpireDue(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	result := r.getDB(ctx).Exec(`
		UPDATE transactions
		SET status = ?, expired_at = ?, updated_at = ?
		WHERE status = ? AND id IN (
			SELECT id FROM transactions
			WHERE status = ? AND deleted_at IS NULL AND invoice_expires_at IS NOT NULL AND invoice_expires_at <= ?
			ORDER BY invoice_expires_at
			LIMIT ?
			FOR UPDATE SKIP LOCKED
		)`, domain.TransactionStatusExpired, now, now, domain.TransactionStatusPending,
		domain.TransactionStatusPending, now, limit)
	return result.RowsAffected, result.Error
}

func (r *transactionRepository) ClaimPaymentLinkEmail(ctx context.Context, id uuid.UUID, sentAt time.Time) (bool, error) {
	result := r.getDB(ctx).Model(&domain.Transaction{}).Where("id = ? AND payment_link_sent_at IS NULL AND status = ?", id, "pending").Update("payment_link_sent_at", sentAt)
	return result.RowsAffected == 1, result.Error
}

func (r *transactionRepository) ClaimReminderEmail(ctx context.Context, id uuid.UUID, sentAt time.Time, intervalDays int) (bool, error) {
	cutoff := sentAt.AddDate(0, 0, -intervalDays)
	result := r.getDB(ctx).Model(&domain.Transaction{}).Where("id = ? AND status = 'pending' AND (last_reminder_sent_at IS NULL OR last_reminder_sent_at <= ?)", id, cutoff).
		Updates(map[string]interface{}{"last_reminder_sent_at": sentAt, "reminder_count": gorm.Expr("reminder_count + 1")})
	return result.RowsAffected == 1, result.Error
}
