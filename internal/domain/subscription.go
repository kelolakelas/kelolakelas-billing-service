package domain

import (
	"time"

	"github.com/google/uuid"
)

const (
	BillingCycleMonthly   = "monthly"
	BillingCycleQuarterly = "quarterly"
	BillingCycleYearly    = "yearly"
)

type Subscription struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	EnrollmentID    uuid.UUID `gorm:"type:uuid;not null;unique" json:"enrollment_id"`
	TenantID        uuid.UUID `gorm:"type:uuid;index" json:"tenant_id"`
	ParentID        uuid.UUID `gorm:"type:uuid;index" json:"parent_id"`
	StudentID       uuid.UUID `gorm:"type:uuid;index" json:"student_id"`
	BillingCycle    string    `gorm:"type:varchar(50);not null" json:"billing_cycle"`
	NextBillingDate time.Time `gorm:"type:date;not null" json:"next_billing_date"`
	Status          string    `gorm:"type:varchar(50);not null;default:active;index" json:"status"`
	BillingEmail    string    `gorm:"type:varchar(255)" json:"billing_email"`
	ParentName      string    `gorm:"type:varchar(255)" json:"parent_name"`
	ClassName       string    `gorm:"type:varchar(255)" json:"class_name"`
	Amount          int64     `gorm:"type:bigint;not null;default:0" json:"amount"`
	CreatedAt       time.Time `gorm:"type:timestamp;not null;default:now()" json:"created_at"`
	UpdatedAt       time.Time `gorm:"type:timestamp;not null;default:now()" json:"updated_at"`
}
