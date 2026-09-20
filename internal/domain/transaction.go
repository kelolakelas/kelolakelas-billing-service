package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrInvalidWebhookSignature  = errors.New("invalid Duitku callback signature")
	ErrTransactionNotFound      = errors.New("transaction not found")
	ErrTransactionAlreadyPaid   = errors.New("transaction already paid")
	ErrInvalidTransactionStatus = errors.New("invalid transaction status")
)

// Transaction status values. `expired` is terminal for unpaid invoices: a later
// paid callback is still accepted by the billing service and moves the row to
// `paid` so a genuine late payment is never lost. `failed` and `expired` keep the
// payment recoverable by a replacement invoice, while `cancelled` is written only
// when the parent withdraws an unpaid enrollment: no replacement invoice is ever
// issued for it, and its seat release stays enqueued.
const (
	TransactionStatusPending   = "pending"
	TransactionStatusPaid      = "paid"
	TransactionStatusFailed    = "failed"
	TransactionStatusExpired   = "expired"
	TransactionStatusCancelled = "cancelled"
	TransactionStatusRefunded  = "refunded"
	TransactionStatusCreating  = "creating"
)

// Duitku callback result codes. `00` is a successful payment, `01`/`02` are
// failures, and any other value is unknown and must not change local state.
const (
	ResultCodeSuccess  = "00"
	ResultCodeFailed   = "01"
	ResultCodeCanceled = "02"
)

// DefaultInvoiceValidityMinutes is the invoice validity requested from the
// payment gateway when no explicit period is configured (24 hours). It matches
// the Duitku adapter default so the stored expiry mirrors the requested one.
const DefaultInvoiceValidityMinutes = 1440

// InvoiceExpiresAt returns the local expiry deadline for an invoice requested
// with the given validity in minutes. A non-positive value falls back to
// DefaultInvoiceValidityMinutes so a misconfigured duration can never produce an
// already expired invoice.
func InvoiceExpiresAt(now time.Time, validityMinutes int) time.Time {
	if validityMinutes <= 0 {
		validityMinutes = DefaultInvoiceValidityMinutes
	}
	return now.Add(time.Duration(validityMinutes) * time.Minute)
}

// IsKnownResultCode reports whether the callback result code has a documented
// meaning. Unknown codes must be logged and must not change transaction state.
// Source: _docs/duitku/api.md section 7.
func IsKnownResultCode(code string) bool {
	switch code {
	case ResultCodeSuccess, ResultCodeFailed, ResultCodeCanceled:
		return true
	default:
		return false
	}
}

type Transaction struct {
	ID                     uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	MerchantOrderID        string         `gorm:"type:varchar(255);unique;not null" json:"merchant_order_id"`
	TenantID               uuid.UUID      `gorm:"type:uuid;not null;index" json:"tenant_id"`     // Cross-service
	ParentID               uuid.UUID      `gorm:"type:uuid;not null;index" json:"parent_id"`     // Cross-service
	StudentID              uuid.UUID      `gorm:"type:uuid;not null;index" json:"student_id"`    // Cross-service
	EnrollmentID           uuid.UUID      `gorm:"type:uuid;not null;index" json:"enrollment_id"` // Cross-service
	VoucherID              *uuid.UUID     `gorm:"type:uuid;index" json:"voucher_id,omitempty"`   // In-service
	SubtotalAmount         int64          `gorm:"type:bigint;not null" json:"subtotal_amount"`
	DiscountAmount         int64          `gorm:"type:bigint;not null;default:0" json:"discount_amount"`
	GrossAmount            int64          `gorm:"type:bigint;not null" json:"gross_amount"`
	PlatformFee            int64          `gorm:"type:bigint;not null" json:"platform_fee"`
	PaymentGatewayFee      int64          `gorm:"type:bigint;not null;default:0" json:"payment_gateway_fee"`
	NetAmount              int64          `gorm:"type:bigint;not null" json:"net_amount"`
	SubscriptionID         *uuid.UUID     `gorm:"type:uuid;index" json:"subscription_id,omitempty"`
	BillingPeriodStart     *time.Time     `gorm:"type:date;index" json:"billing_period_start,omitempty"`
	Currency               string         `gorm:"type:varchar(50);not null;default:'IDR'" json:"currency"`
	Status                 string         `gorm:"type:varchar(255);not null" json:"status"`
	IsSandbox              bool           `gorm:"type:boolean;not null;default:false" json:"is_sandbox"`
	PaymentGatewayProvider *string        `gorm:"type:varchar(255);default:'duitku'" json:"payment_gateway_provider,omitempty"`
	PaymentMethod          *string        `gorm:"type:varchar(255)" json:"payment_method,omitempty"`
	PaymentIntentID        *string        `gorm:"type:varchar(255);unique;index" json:"payment_intent_id,omitempty"`
	CheckoutSessionURL     *string        `gorm:"type:text" json:"checkout_session_url,omitempty"`
	BillingEmail           string         `gorm:"type:varchar(255)" json:"billing_email,omitempty"`
	PaymentLinkSentAt      *time.Time     `gorm:"type:timestamp" json:"payment_link_sent_at,omitempty"`
	LastReminderSentAt     *time.Time     `gorm:"type:timestamp" json:"last_reminder_sent_at,omitempty"`
	ReminderCount          int            `gorm:"type:int;not null;default:0" json:"reminder_count"`
	InvoiceExpiresAt       *time.Time     `gorm:"type:timestamp;index" json:"invoice_expires_at,omitempty"`
	ExpiredAt              *time.Time     `gorm:"type:timestamp" json:"expired_at,omitempty"`
	PaidAt                 *time.Time     `gorm:"type:timestamp" json:"paid_at,omitempty"`
	CreatedAt              time.Time      `gorm:"type:timestamp;not null;default:now()" json:"created_at"`
	UpdatedAt              time.Time      `gorm:"type:timestamp;not null;default:now()" json:"updated_at"`
	DeletedAt              gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`

	Voucher        *Voucher               `gorm:"foreignKey:VoucherID" json:"voucher,omitempty"`
	Reconciliation *PaymentReconciliation `gorm:"foreignKey:TransactionID" json:"reconciliation,omitempty"`
}

// CancelEnrollmentPaymentRequest asks the billing service to mark the unpaid
// transaction of an enrollment as cancelled. The enrollment is the stable key the
// academic service holds, so the request never has to know a transaction ID.
type CancelEnrollmentPaymentRequest struct {
	EnrollmentID uuid.UUID `json:"enrollment_id" binding:"required"`
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
	ID                          uuid.UUID  `json:"id"`
	MerchantOrderID             string     `json:"merchant_order_id"`
	TenantID                    uuid.UUID  `json:"tenant_id"`
	ParentID                    uuid.UUID  `json:"parent_id"`
	StudentID                   uuid.UUID  `json:"student_id"`
	EnrollmentID                uuid.UUID  `json:"enrollment_id"`
	VoucherID                   *uuid.UUID `json:"voucher_id,omitempty"`
	SubtotalAmount              int64      `json:"subtotal_amount"`
	DiscountAmount              int64      `json:"discount_amount"`
	GrossAmount                 int64      `json:"gross_amount"`
	PlatformFee                 int64      `json:"platform_fee"`
	PaymentGatewayFee           int64      `json:"payment_gateway_fee"`
	NetAmount                   int64      `json:"net_amount"`
	Currency                    string     `json:"currency"`
	Status                      string     `json:"status"`
	PaymentGatewayProvider      string     `json:"payment_gateway_provider,omitempty"`
	PaymentIntentID             string     `json:"payment_intent_id,omitempty"`
	CheckoutSessionURL          string     `json:"checkout_session_url,omitempty"`
	InvoiceExpiresAt            *time.Time `json:"invoice_expires_at,omitempty"`
	ExpiredAt                   *time.Time `json:"expired_at,omitempty"`
	PaidAt                      *time.Time `json:"paid_at,omitempty"`
	CreatedAt                   time.Time  `json:"created_at"`
	UpdatedAt                   time.Time  `json:"updated_at"`
	ReconciliationStatus        string     `json:"reconciliation_status,omitempty"`
	ReconciliationKind          string     `json:"reconciliation_kind,omitempty"`
	ReconciliationAttempts      int        `json:"reconciliation_attempts,omitempty"`
	ReconciliationLastError     string     `json:"reconciliation_last_error,omitempty"`
	ReconciliationNextAttemptAt *time.Time `json:"reconciliation_next_attempt_at,omitempty"`
}

type TransactionQuery struct {
	Page         int
	PageSize     int
	Status       string
	TenantID     *uuid.UUID
	ParentID     *uuid.UUID
	StudentID    *uuid.UUID
	EnrollmentID *uuid.UUID
	DateFrom     *time.Time
	DateTo       *time.Time
	Search       string
}
type TransactionListResponse struct {
	Items      []TransactionResponse `json:"items"`
	Pagination struct {
		Page       int   `json:"page"`
		PageSize   int   `json:"page_size"`
		TotalItems int64 `json:"total_items"`
		TotalPages int   `json:"total_pages"`
	} `json:"pagination"`
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
