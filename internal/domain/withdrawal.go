package domain

import (
	"time"

	"github.com/google/uuid"
)

type Withdrawal struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID         uuid.UUID  `gorm:"type:uuid;not null;index" json:"tenant_id"` // Cross-service
	BankAccountID    uuid.UUID  `gorm:"type:uuid;not null;index" json:"bank_account_id"`
	Amount           int64      `gorm:"type:bigint;not null" json:"amount"`
	AdminFee         int64      `gorm:"type:bigint;not null;default:0" json:"admin_fee"`
	NetAmount        int64      `gorm:"type:bigint;not null" json:"net_amount"`
	Status           string     `gorm:"type:varchar(255);not null" json:"status"`
	ProviderPayoutID *string    `gorm:"type:varchar(255);unique;index" json:"provider_payout_id,omitempty"`
	RequestedAt      time.Time  `gorm:"type:timestamp;not null;default:now()" json:"requested_at"`
	ProcessedAt      *time.Time `gorm:"type:timestamp" json:"processed_at,omitempty"`

	BankAccount *BankAccount `gorm:"foreignKey:BankAccountID" json:"bank_account,omitempty"`
}
