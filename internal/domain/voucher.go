package domain

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Voucher struct {
	ID                   uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID             uuid.UUID      `gorm:"type:uuid;not null;index" json:"tenant_id"` // Cross-service
	Code                 string         `gorm:"type:varchar(255);not null" json:"code"`
	DiscountType         string         `gorm:"type:varchar(255);not null" json:"discount_type"`
	DiscountValue        float64        `gorm:"type:numeric(15,2);not null" json:"discount_value"`
	MaxDiscountAmount    *int64         `gorm:"type:bigint" json:"max_discount_amount,omitempty"`
	MinTransactionAmount int64          `gorm:"type:bigint;not null;default:0" json:"min_transaction_amount"`
	MaxUses              *int           `gorm:"type:integer" json:"max_uses,omitempty"`
	CurrentUses          int            `gorm:"type:integer;not null;default:0" json:"current_uses"`
	ValidFrom            *time.Time     `gorm:"type:timestamp" json:"valid_from,omitempty"`
	ValidUntil           *time.Time     `gorm:"type:timestamp" json:"valid_until,omitempty"`
	IsActive             bool           `gorm:"type:boolean;not null;default:true;index" json:"is_active"`
	CreatedAt            time.Time      `gorm:"type:timestamp;not null;default:now()" json:"created_at"`
	UpdatedAt            time.Time      `gorm:"type:timestamp;not null;default:now()" json:"updated_at"`
	DeletedAt            gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}
