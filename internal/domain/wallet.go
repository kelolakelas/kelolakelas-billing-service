package domain

import (
	"time"

	"github.com/google/uuid"
)

type Wallet struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID         uuid.UUID `gorm:"type:uuid;not null;unique;index" json:"tenant_id"` // Cross-service, ordinary UUID
	AvailableBalance int64     `gorm:"type:bigint;not null;default:0" json:"available_balance"`
	PendingBalance   int64     `gorm:"type:bigint;not null;default:0" json:"pending_balance"`
	UpdatedAt        time.Time `gorm:"type:timestamp;not null;default:now()" json:"updated_at"`
}
