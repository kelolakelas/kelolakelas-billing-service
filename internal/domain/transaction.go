package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrInvalidWebhookToken    = errors.New("invalid flip validation token")
	ErrTransactionNotFound    = errors.New("transaction not found")
	ErrTransactionAlreadyPaid = errors.New("transaction already paid")
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
	PaymentGatewayFee      int64          `gorm:"type:bigint;not null;default:0" json:"payment_gateway_fee"`
	NetAmount              int64          `gorm:"type:bigint;not null" json:"net_amount"`
	SubscriptionID         *uuid.UUID     `gorm:"type:uuid;index" json:"subscription_id,omitempty"`
	Currency               string         `gorm:"type:varchar(50);not null;default:'IDR'" json:"currency"`
	Status                 string         `gorm:"type:varchar(255);not null" json:"status"`
	IsSandbox              bool           `gorm:"type:boolean;not null;default:false" json:"is_sandbox"`
	PaymentGatewayProvider *string        `gorm:"type:varchar(255)" json:"payment_gateway_provider,omitempty"`
	PaymentMethod          *string        `gorm:"type:varchar(255)" json:"payment_method,omitempty"`
	PaymentIntentID        *string        `gorm:"type:varchar(255);unique;index" json:"payment_intent_id,omitempty"`
	CheckoutSessionURL     *string        `gorm:"type:text" json:"checkout_session_url,omitempty"`
	PaidAt                 *time.Time     `gorm:"type:timestamp" json:"paid_at,omitempty"`
	CreatedAt              time.Time      `gorm:"type:timestamp;not null;default:now()" json:"created_at"`
	UpdatedAt              time.Time      `gorm:"type:timestamp;not null;default:now()" json:"updated_at"`
	DeletedAt              gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`

	Voucher *Voucher `gorm:"foreignKey:VoucherID" json:"voucher,omitempty"`
}

type GenerateSubscriptionPaymentRequest struct {
	TenantID          uuid.UUID  `json:"tenant_id" binding:"required"`
	StudentID         uuid.UUID  `json:"student_id" binding:"required"`
	ClassID           uuid.UUID  `json:"class_id" binding:"required"`
	EnrollmentID      uuid.UUID  `json:"enrollment_id" binding:"required"`
	ParentID          uuid.UUID  `json:"parent_id" binding:"required"`
	BillingCycle      string     `json:"billing_cycle" binding:"required,oneof=monthly quarterly yearly"`
	VoucherID         *uuid.UUID `json:"voucher_id,omitempty"`
	SubtotalAmount    int64      `json:"subtotal_amount" binding:"required,gt=0"`
	DiscountAmount    int64      `json:"discount_amount" binding:"gte=0"`
	PlatformFee       int64      `json:"platform_fee" binding:"gte=0"`
	PaymentGatewayFee int64      `json:"payment_gateway_fee" binding:"gte=0"`
	Title             string     `json:"title"`
	SenderName        string     `json:"sender_name"`
	SenderEmail       string     `json:"sender_email"`
	SenderPhone       string     `json:"sender_phone"`
}

type GenerateSubscriptionPaymentResponse struct {
	TransactionID      uuid.UUID `json:"transaction_id"`
	CheckoutSessionURL string    `json:"checkout_session_url"`
	PaymentIntentID    string    `json:"payment_intent_id"`
	GrossAmount        int64     `json:"gross_amount"`
	Status             string    `json:"status"`
}

type TransactionResponse struct {
	ID                     uuid.UUID  `json:"id"`
	TenantID               uuid.UUID  `json:"tenant_id"`
	EnrollmentID           uuid.UUID  `json:"enrollment_id"`
	VoucherID              *uuid.UUID `json:"voucher_id,omitempty"`
	SubtotalAmount         int64      `json:"subtotal_amount"`
	DiscountAmount         int64      `json:"discount_amount"`
	GrossAmount            int64      `json:"gross_amount"`
	PlatformFee            int64      `json:"platform_fee"`
	PaymentGatewayFee      int64      `json:"payment_gateway_fee"`
	NetAmount              int64      `json:"net_amount"`
	Currency               string     `json:"currency"`
	Status                 string     `json:"status"`
	PaymentGatewayProvider string     `json:"payment_gateway_provider,omitempty"`
	PaymentIntentID        string     `json:"payment_intent_id,omitempty"`
	CheckoutSessionURL     string     `json:"checkout_session_url,omitempty"`
	PaidAt                 *time.Time `json:"paid_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

type HTTPResponse struct {
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type ErrorResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
