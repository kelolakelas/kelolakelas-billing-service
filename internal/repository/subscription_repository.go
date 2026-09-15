package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type subscriptionRepository struct{ db *gorm.DB }

func NewSubscriptionRepository(db *gorm.DB) SubscriptionRepository {
	return &subscriptionRepository{db: db}
}

func (r *subscriptionRepository) Create(ctx context.Context, subscription *domain.Subscription) error {
	return GetDB(ctx, r.db).Create(subscription).Error
}

func (r *subscriptionRepository) GetByEnrollmentID(ctx context.Context, enrollmentID uuid.UUID) (*domain.Subscription, error) {
	var subscription domain.Subscription
	if err := GetDB(ctx, r.db).First(&subscription, "enrollment_id = ?", enrollmentID).Error; err != nil {
		return nil, err
	}
	return &subscription, nil
}

func (r *subscriptionRepository) Update(ctx context.Context, subscription *domain.Subscription) error {
	return GetDB(ctx, r.db).Save(subscription).Error
}

func (r *subscriptionRepository) ListDueForRenewal(ctx context.Context, before time.Time) ([]domain.Subscription, error) {
	var subscriptions []domain.Subscription
	err := r.db.WithContext(ctx).Where("status = ? AND next_billing_date <= ?", "active", before).Find(&subscriptions).Error
	return subscriptions, err
}
