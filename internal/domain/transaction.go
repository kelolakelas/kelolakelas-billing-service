package domain

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Transaction struct {
	ID                     uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID               uuid.UUID      `gorm:"type:uuid;not null;index" json:"tenant_id"`     // Cross-service
	EnrollmentID           uuid.UUID      `gorm:"type:uuid;not null;index" json:"enrollment_id"` // Cross-service
	VoucherID              *uuid.UUID     `gorm:"type:uuid;index" json:"voucher_id,omitempty"`   // In-service
	SubtotalAmount         int64          `gorm:"type:bigint;not null" json:"subtotal_amount"`
	DiscountAmount         int64          `gorm:"type:bigint;not null;default:0" json:"discount_amount"`
	GrossAmount            int64          `gorm:"type:bigint;not null" json:"gross_amount"`
	PlatformFee            int64          `gorm:"type:bigint;not null" json:"platform_fee"`
	NetAmount              int64          `gorm:"type:bigint;not null" json:"net_amount"`
	Currency               string         `gorm:"type:varchar(50);not null;default:'IDR'" json:"currency"`
	Status                 string         `gorm:"type:varchar(255);not null" json:"status"`
	IsSandbox              bool           `gorm:"type:boolean;not null;default:false" json:"is_sandbox"`
	PaymentGatewayProvider *string        `gorm:"type:varchar(255)" json:"payment_gateway_provider,omitempty"`
	PaymentIntentID        *string        `gorm:"type:varchar(255);unique;index" json:"payment_intent_id,omitempty"`
	CheckoutSessionURL     *string        `gorm:"type:text" json:"checkout_session_url,omitempty"`
	PaidAt                 *time.Time     `gorm:"type:timestamp" json:"paid_at,omitempty"`
	CreatedAt              time.Time      `gorm:"type:timestamp;not null;default:now()" json:"created_at"`
	UpdatedAt              time.Time      `gorm:"type:timestamp;not null;default:now()" json:"updated_at"`
	DeletedAt              gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`

	Voucher *Voucher `gorm:"foreignKey:VoucherID" json:"voucher,omitempty"`
}
