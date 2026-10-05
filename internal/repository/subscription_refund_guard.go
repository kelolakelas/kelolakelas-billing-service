package repository

import (
	"context"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Serialize already-selected renewal/lifecycle work with cancellation. A stale
// worker must not issue an invoice or resume after refund commits.
func (r *subscriptionRepository) GuardSubscription(ctx context.Context, id uuid.UUID, call func(context.Context) error) error {
	return GetDB(ctx, r.db).Transaction(func(db *gorm.DB) error {
		var sub domain.Subscription
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sub, "id = ?", id).Error; err != nil {
			return err
		}
		if sub.Status == "cancelled" {
			return nil
		}
		return call(context.WithValue(ctx, txKey{}, db))
	})
}
