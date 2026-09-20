package domain

import (
	"time"

	"github.com/google/uuid"
)

const (
	ReconciliationStatusPending        = "pending"
	ReconciliationStatusProcessing     = "processing"
	ReconciliationStatusActive         = "active"
	ReconciliationStatusTerminalFailed = "terminal_failed"
)

// Reconciliation kinds. A transaction owes Academic exactly one durable side
// effect at a time: activation once it is paid, or release once it failed or
// expired. The unique index on transaction_id keeps a single row per transaction,
// so a late paid callback rewrites a release job back into an activation job and
// the mismatch stays visible to operators instead of silently passing.
const (
	ReconciliationKindActivation = "activation"
	ReconciliationKindRelease    = "release"
)

// PaymentReconciliation records the durable side effect of activating an academic enrollment.
// It is intentionally owned by billing because billing commits the paid transaction first.
type PaymentReconciliation struct {
	ID            uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TransactionID uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex" json:"transaction_id"`
	EnrollmentID  uuid.UUID  `gorm:"type:uuid;not null;index" json:"enrollment_id"`
	Kind          string     `gorm:"type:varchar(20);not null;default:activation;index" json:"kind"`
	Status        string     `gorm:"type:varchar(30);not null;index" json:"status"`
	AttemptCount  int        `gorm:"type:int;not null;default:0" json:"attempt_count"`
	NextAttemptAt *time.Time `gorm:"type:timestamp;index" json:"next_attempt_at,omitempty"`
	LastAttemptAt *time.Time `gorm:"type:timestamp" json:"last_attempt_at,omitempty"`
	LastError     *string    `gorm:"type:text" json:"last_error,omitempty"`
	CompletedAt   *time.Time `gorm:"type:timestamp" json:"completed_at,omitempty"`
	CreatedAt     time.Time  `gorm:"type:timestamp;not null;default:now()" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"type:timestamp;not null;default:now()" json:"updated_at"`
}

func (PaymentReconciliation) TableName() string { return "payment_reconciliations" }
