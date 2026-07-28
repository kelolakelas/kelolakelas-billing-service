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
