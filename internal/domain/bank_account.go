package domain

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BankAccount struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID      uuid.UUID      `gorm:"type:uuid;not null;index" json:"tenant_id"` // Cross-service
	BankCode      string         `gorm:"type:varchar(255);not null" json:"bank_code"`
	AccountNumber string         `gorm:"type:varchar(255);not null" json:"account_number"`
	AccountName   string         `gorm:"type:varchar(255);not null" json:"account_name"`
	IsPrimary     bool           `gorm:"type:boolean;not null;default:true" json:"is_primary"`
	CreatedAt     time.Time      `gorm:"type:timestamp;not null;default:now()" json:"created_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}
