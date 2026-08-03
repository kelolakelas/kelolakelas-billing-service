package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type transactionRepository struct {
	db *gorm.DB
}

func NewTransactionRepository(db *gorm.DB) TransactionRepository {
	return &transactionRepository{db: db}
}

func (r *transactionRepository) Create(ctx context.Context, transaction *domain.Transaction) error {
	return r.db.WithContext(ctx).Create(transaction).Error
}

func (r *transactionRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := r.db.WithContext(ctx).First(&tx, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &tx, nil
}

func (r *transactionRepository) GetByPaymentIntentID(ctx context.Context, paymentIntentID string) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := r.db.WithContext(ctx).First(&tx, "payment_intent_id = ?", paymentIntentID).Error; err != nil {
		return nil, err
	}
	return &tx, nil
}

func (r *transactionRepository) List(ctx context.Context, tenantID, parentID *uuid.UUID, query domain.TransactionQuery) ([]domain.Transaction, int64, error) {
	db := r.db.WithContext(ctx).Model(&domain.Transaction{})
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
	return r.db.WithContext(ctx).Save(transaction).Error
}
