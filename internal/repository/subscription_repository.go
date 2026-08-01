package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type subscriptionRepository struct{ db *gorm.DB }

func NewSubscriptionRepository(db *gorm.DB) SubscriptionRepository {
	return &subscriptionRepository{db: db}
}

func (r *subscriptionRepository) Create(ctx context.Context, subscription *domain.Subscription) error {
	return r.db.WithContext(ctx).Create(subscription).Error
}

func (r *subscriptionRepository) GetByEnrollmentID(ctx context.Context, enrollmentID uuid.UUID) (*domain.Subscription, error) {
	var subscription domain.Subscription
	if err := r.db.WithContext(ctx).First(&subscription, "enrollment_id = ?", enrollmentID).Error; err != nil {
		return nil, err
	}
	return &subscription, nil
}

func (r *subscriptionRepository) Update(ctx context.Context, subscription *domain.Subscription) error {
	return r.db.WithContext(ctx).Save(subscription).Error
}
