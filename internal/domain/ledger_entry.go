package domain

import (
	"time"

	"github.com/google/uuid"
)

type LedgerEntry struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	WalletID      uuid.UUID `gorm:"type:uuid;not null;index" json:"wallet_id"`
	ReferenceID   uuid.UUID `gorm:"type:uuid;not null;index" json:"reference_id"`
	ReferenceType string    `gorm:"type:varchar(255);not null" json:"reference_type"`
	Amount        int64     `gorm:"type:bigint;not null" json:"amount"`
	EntryType     string    `gorm:"type:varchar(255);not null" json:"entry_type"`
	Description   *string   `gorm:"type:text" json:"description,omitempty"`
	CreatedAt     time.Time `gorm:"type:timestamp;not null;default:now()" json:"created_at"`

	Wallet *Wallet `gorm:"foreignKey:WalletID" json:"wallet,omitempty"`
}

// LedgerEntryResponse is one row of the tenant-visible mutation list (KEL-142).
// Entries are append-only financial evidence: they are never updated or
// deleted, only listed newest first.
type LedgerEntryResponse struct {
	ID            uuid.UUID `json:"id"`
	ReferenceID   uuid.UUID `json:"reference_id"`
	ReferenceType string    `json:"reference_type"`
	Amount        int64     `json:"amount"`
	EntryType     string    `json:"entry_type"`
	Description   *string   `json:"description,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// LedgerQuery carries the ledger pagination the handler parsed.
type LedgerQuery struct {
	Page     int
	PageSize int
}

// LedgerListResponse mirrors TransactionListResponse pagination.
type LedgerListResponse struct {
	Items      []LedgerEntryResponse `json:"items"`
	Pagination struct {
		Page       int   `json:"page"`
		PageSize   int   `json:"page_size"`
		TotalItems int64 `json:"total_items"`
		TotalPages int   `json:"total_pages"`
	} `json:"pagination"`
}
