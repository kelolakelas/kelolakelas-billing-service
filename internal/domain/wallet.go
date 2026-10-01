package domain

import (
	"time"

	"github.com/google/uuid"
)

type Wallet struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID         uuid.UUID `gorm:"type:uuid;not null;unique;index" json:"tenant_id"` // Cross-service, ordinary UUID
	AvailableBalance int64     `gorm:"type:bigint;not null;default:0" json:"available_balance"`
	PendingBalance   int64     `gorm:"type:bigint;not null;default:0" json:"pending_balance"`
	UpdatedAt        time.Time `gorm:"type:timestamp;not null;default:now()" json:"updated_at"`
}

// WalletBalanceResponse is what a tenant member reads (KEL-142). It mirrors the
// stored wallet row: the Duitku paid callback credits AvailableBalance by the
// transaction NetAmount and writes one `payment_received` ledger entry for the
// same amount, so on payment-only data the balance equals the ledger sum.
// Sandbox callbacks never credit the wallet or the ledger, so sandbox payments
// stay invisible here.
type WalletBalanceResponse struct {
	AvailableBalance int64 `json:"available_balance"`
	PendingBalance   int64 `json:"pending_balance"`
}
