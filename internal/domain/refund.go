package domain

import (
	"github.com/google/uuid"
	"time"
)

type RefundRequest struct {
	Reason            string `json:"reason" binding:"required"`
	TransferReference string `json:"transfer_reference" binding:"required"`
}

type TransactionRefund struct {
	TransactionID     uuid.UUID  `gorm:"type:uuid;primaryKey" json:"transaction_id"`
	TenantID          uuid.UUID  `gorm:"type:uuid" json:"tenant_id"`
	ActorID           uuid.UUID  `gorm:"type:uuid" json:"actor_id"`
	Reason            string     `json:"reason"`
	TransferReference string     `json:"transfer_reference"`
	CreatedAt         time.Time  `json:"created_at"`
	Status            string     `json:"status"`
	AttemptCount      int        `json:"attempt_count"`
	NextAttemptAt     *time.Time `json:"next_attempt_at,omitempty"`
	LastError         *string    `json:"last_error,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}
