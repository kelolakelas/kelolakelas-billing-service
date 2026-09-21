package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Errors the internal reconciliation endpoint reports. They are separate sentinels so
// the handler can answer 400 for an unknown status filter and 503 for a service that was
// started without a reconciliation repository, instead of collapsing both into a 500 that
// tells an operator nothing about which mistake was made.
var (
	ErrInvalidReconciliationStatus = errors.New("invalid reconciliation status")
	ErrReconciliationUnavailable   = errors.New("reconciliation store is unavailable")
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

// IsReconciliationStatusValue reports whether a reconciliation status is one the
// code can write. The comparison is exact so the internal reconciliation list
// endpoint refuses an unknown status instead of answering an empty page that reads
// like "nothing failed" when the operator actually asked for something the service
// does not have.
func IsReconciliationStatusValue(status string) bool {
	for _, candidate := range ReconciliationStatusValues() {
		if status == candidate {
			return true
		}
	}
	return false
}

// ReconciliationStatusValues lists every status the reconciliation state machine
// writes. It is the accepted set for the internal list endpoint, so a status an
// operator observes on a row can always be asked for by name.
func ReconciliationStatusValues() []string {
	return []string{
		ReconciliationStatusPending,
		ReconciliationStatusProcessing,
		ReconciliationStatusActive,
		ReconciliationStatusTerminalFailed,
	}
}

// ReconciliationListResponse is the internal operator view of reconciliation rows. It is
// deliberately the raw domain rows: an operator diagnosing a stuck job needs the attempt
// count, the stored failure, and the timestamps, and reshaping them into a
// parent-facing projection would drop exactly that evidence.
type ReconciliationListResponse struct {
	Items []PaymentReconciliation `json:"items"`
}

// ReconciliationRequeueResponse reports how many rows a re-drive actually moved. The
// count is the number the database changed, so a call that matched nothing answers zero
// rather than the number of rows that were merely eligible.
type ReconciliationRequeueResponse struct {
	Requeued   int64     `json:"requeued"`
	RequeuedAt time.Time `json:"requeued_at"`
}
