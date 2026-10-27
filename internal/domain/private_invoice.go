package domain

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// PrivateInvoiceScope cannot read a transaction outside its exact request purchase.
type PrivateInvoiceScope struct {
	TransactionID uuid.UUID `json:"transaction_id" binding:"required"`
	EnrollmentID  uuid.UUID `json:"enrollment_id" binding:"required"`
	TenantID      uuid.UUID `json:"tenant_id" binding:"required"`
	ParentID      uuid.UUID `json:"parent_id" binding:"required"`
	StudentID     uuid.UUID `json:"student_id" binding:"required"`
}
type PrivateInvoice struct {
	ID                 uuid.UUID  `json:"id"`
	EnrollmentID       uuid.UUID  `json:"enrollment_id"`
	TenantID           uuid.UUID  `json:"tenant_id"`
	ParentID           uuid.UUID  `json:"parent_id"`
	StudentID          uuid.UUID  `json:"student_id"`
	Status             string     `json:"status"`
	CheckoutSessionURL *string    `json:"checkout_session_url"`
	InvoiceExpiresAt   *time.Time `json:"invoice_expires_at"`
}
type PrivateInvoiceReader interface {
	ReadPrivateInvoice(context.Context, PrivateInvoiceScope) (*PrivateInvoice, error)
}
