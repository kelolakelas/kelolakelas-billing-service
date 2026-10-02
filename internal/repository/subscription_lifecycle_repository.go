package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SubscriptionLifecycleRepository owns the subscription transition and its durable
// Academic side effect. A callback and the renewal worker share the subscription row lock.
type SubscriptionLifecycleRepository interface {
	SuspendOverdue(context.Context, uuid.UUID, time.Time, time.Time) (bool, error)
	ResumePaid(context.Context, uuid.UUID, uuid.UUID, time.Time) error
	ClaimLifecycle(context.Context, time.Time) (*SubscriptionLifecycle, error)
	FinishLifecycle(context.Context, *SubscriptionLifecycle, time.Time, string, int) error
	ListLifecycle(context.Context, string, int) ([]SubscriptionLifecycle, error)
}

type SubscriptionLifecycle struct {
	SubscriptionID uuid.UUID  `gorm:"type:uuid;primaryKey" json:"subscription_id"`
	EnrollmentID   uuid.UUID  `gorm:"type:uuid" json:"enrollment_id"`
	DesiredAction  string     `gorm:"type:varchar(20)" json:"desired_action"`
	Status         string     `gorm:"type:varchar(30)" json:"status"`
	AttemptCount   int        `json:"attempt_count"`
	NextAttemptAt  *time.Time `json:"next_attempt_at,omitempty"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastError      *string    `json:"last_error,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (SubscriptionLifecycle) TableName() string { return "subscription_lifecycle_reconciliations" }

// SuspendOverdue checks paid status after locking the subscription. No network call
// occurs in this transaction; even simultaneous workers can create only one job.
func (r *subscriptionRepository) SuspendOverdue(ctx context.Context, id uuid.UUID, period, now time.Time) (bool, error) {
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var sub domain.Subscription
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sub, "id = ?", id).Error; err != nil {
			return err
		}
		if sub.Status != "active" || !dateEqual(sub.NextBillingDate, period) {
			return nil
		}
		var paid int64
		if err := db.Model(&domain.Transaction{}).Where("subscription_id = ? AND billing_period_start = ? AND status = ?", id, period, domain.TransactionStatusPaid).Count(&paid).Error; err != nil {
			return err
		}
		if paid != 0 {
			return nil
		}
		if err := db.Model(&sub).Update("status", "suspended").Error; err != nil {
			return err
		}
		job := SubscriptionLifecycle{SubscriptionID: id, EnrollmentID: sub.EnrollmentID, DesiredAction: "suspend", Status: "pending", NextAttemptAt: &now}
		if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "subscription_id"}}, DoUpdates: clause.Assignments(map[string]interface{}{"desired_action": "suspend", "status": "pending", "next_attempt_at": now, "attempt_count": 0, "last_error": nil})}).Create(&job).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

func (r *subscriptionRepository) ListLifecycle(ctx context.Context, status string, limit int) ([]SubscriptionLifecycle, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	var jobs []SubscriptionLifecycle
	query := r.db.WithContext(ctx).Order("updated_at DESC").Limit(limit)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	return jobs, query.Find(&jobs).Error
}

func dateEqual(a, b time.Time) bool { return a.Format("2006-01-02") == b.Format("2006-01-02") }

// ResumePaid is called inside the webhook's transaction. If a suspend is in
// flight, its completion will turn this desired resume into a pending job.
func (r *subscriptionRepository) ResumePaid(ctx context.Context, id, enrollmentID uuid.UUID, now time.Time) error {
	db := GetDB(ctx, r.db)
	var sub domain.Subscription
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sub, "id = ?", id).Error; err != nil {
		return err
	}
	if sub.Status != "suspended" {
		return nil
	}
	if err := db.Model(&sub).Update("status", "active").Error; err != nil {
		return err
	}
	job := SubscriptionLifecycle{SubscriptionID: id, EnrollmentID: enrollmentID, DesiredAction: "resume", Status: "pending", NextAttemptAt: &now}
	return db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "subscription_id"}}, DoUpdates: clause.Assignments(map[string]interface{}{
		"desired_action": "resume", "status": gorm.Expr("CASE WHEN subscription_lifecycle_reconciliations.status = 'processing' THEN 'processing' ELSE 'pending' END"),
		"next_attempt_at": now, "attempt_count": 0, "last_error": nil,
	})}).Create(&job).Error
}

func (r *subscriptionRepository) ClaimLifecycle(ctx context.Context, now time.Time) (*SubscriptionLifecycle, error) {
	var job SubscriptionLifecycle
	err := r.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		err := db.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("(status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?)) OR (status = 'processing' AND last_attempt_at < ?)", now, now.Add(-5*time.Minute)).
			Order("updated_at ASC").First(&job).Error
		if err != nil {
			return err
		}
		return db.Model(&job).Updates(map[string]interface{}{"status": "processing", "attempt_count": gorm.Expr("attempt_count + 1"), "last_attempt_at": now}).Error
	})
	if err != nil {
		return nil, err
	}
	job.AttemptCount++
	job.Status = "processing"
	job.LastAttemptAt = &now
	return &job, nil
}

// A stale suspend completion may not mark a newer resume complete. A lease
// expiry can replay an idempotent Academic call after a process crash.
func (r *subscriptionRepository) FinishLifecycle(ctx context.Context, claimed *SubscriptionLifecycle, now time.Time, failure string, maxAttempts int) error {
	if claimed == nil {
		return errors.New("missing lifecycle job")
	}
	return r.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var current SubscriptionLifecycle
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "subscription_id = ?", claimed.SubscriptionID).Error; err != nil {
			return err
		}
		if current.Status != "processing" || current.LastAttemptAt == nil || !current.LastAttemptAt.Equal(*claimed.LastAttemptAt) {
			return nil
		}
		if current.DesiredAction != claimed.DesiredAction {
			return db.Model(&current).Updates(map[string]interface{}{"status": "pending", "attempt_count": 0, "next_attempt_at": now, "last_error": nil}).Error
		}
		updates := map[string]interface{}{"status": "active", "next_attempt_at": nil, "last_error": nil, "completed_at": now}
		if failure != "" {
			status := "pending"
			var next interface{} = now.Add(time.Duration(current.AttemptCount) * 5 * time.Minute)
			if maxAttempts > 0 && current.AttemptCount >= maxAttempts {
				status = "terminal_failed"
				next = nil
			}
			updates = map[string]interface{}{"status": status, "next_attempt_at": next, "last_error": failure}
		}
		return db.Model(&current).Updates(updates).Error
	})
}
