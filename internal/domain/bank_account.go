package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	// ErrBankAccountNotFound answers 404: the account does not exist, is soft
	// deleted, or belongs to another tenant. One value for all three so a
	// caller cannot probe which tenants own which accounts.
	ErrBankAccountNotFound = errors.New("bank account not found")
	// ErrBankAccountInvalid answers 400 for empty or overlong fields.
	ErrBankAccountInvalid = errors.New("invalid bank account request")
	// ErrBankAccountInUse answers 409: the primary account still backs a
	// withdrawal in an active status, so deleting it would strand the payout.
	ErrBankAccountInUse = errors.New("bank account is referenced by an active withdrawal")
	// ErrBankAccountConflict answers 409 when two writers race on the primary
	// slot and the storage backstop rejects the loser; the client can retry.
	ErrBankAccountConflict = errors.New("primary account conflict")
)

type BankAccount struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID      uuid.UUID      `gorm:"type:uuid;not null;index" json:"tenant_id"` // Cross-service
	BankCode      string         `gorm:"type:varchar(255);not null" json:"bank_code"`
	AccountNumber string         `gorm:"type:varchar(255);not null" json:"account_number"`
	AccountName   string         `gorm:"type:varchar(255);not null" json:"account_name"`
	IsPrimary     bool           `gorm:"type:boolean;not null" json:"is_primary"`
	CreatedAt     time.Time      `gorm:"type:timestamp;not null;default:now()" json:"created_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

// WithdrawalActiveStatuses are the withdrawal states that still reference their
// destination account: a primary account bound to one of them cannot be
// deleted, because the later platform processing (KEL-143, out of scope here)
// needs the stored destination row.
var WithdrawalActiveStatuses = []string{"requested", "processing"}

// BankAccountResponse is what a tenant member reads (KEL-142). The account
// number is always masked: only the last 4 digits are visible, so a leaked
// response never exposes a full account number.
type BankAccountResponse struct {
	ID            uuid.UUID `json:"id"`
	BankCode      string    `json:"bank_code"`
	AccountNumber string    `json:"account_number"`
	AccountName   string    `json:"account_name"`
	IsPrimary     bool      `json:"is_primary"`
	CreatedAt     time.Time `json:"created_at"`
}

// MaskAccountNumber hides every digit but the last four. Short numbers (4 or
// fewer runes) are fully masked so nothing leaks through a short value.
func MaskAccountNumber(number string) string {
	runes := []rune(number)
	if len(runes) <= 4 {
		return "****"
	}
	masked := make([]rune, len(runes))
	for i := range masked {
		masked[i] = '*'
	}
	copy(masked[len(masked)-4:], runes[len(runes)-4:])
	return string(masked)
}

func ToBankAccountResponse(account *BankAccount) *BankAccountResponse {
	return &BankAccountResponse{
		ID:            account.ID,
		BankCode:      account.BankCode,
		AccountNumber: MaskAccountNumber(account.AccountNumber),
		AccountName:   account.AccountName,
		IsPrimary:     account.IsPrimary,
		CreatedAt:     account.CreatedAt,
	}
}

// BankAccountListResponse lists the tenant's accounts, primary first.
type BankAccountListResponse struct {
	Items []BankAccountResponse `json:"items"`
}

// CreateBankAccountRequest is the tenant write payload (KEL-142). Ownership of
// the account is verified manually by the platform admin at payout processing
// time; the service only validates shape, never ownership.
type CreateBankAccountRequest struct {
	BankCode      string `json:"bank_code" binding:"required,max=255"`
	AccountNumber string `json:"account_number" binding:"required,max=255"`
	AccountName   string `json:"account_name" binding:"required,max=255"`
	IsPrimary     *bool  `json:"is_primary,omitempty"`
}

// UpdateBankAccountRequest patches one account. Nil fields are left unchanged.
type UpdateBankAccountRequest struct {
	BankCode      *string `json:"bank_code,omitempty" binding:"omitempty,max=255"`
	AccountNumber *string `json:"account_number,omitempty" binding:"omitempty,max=255"`
	AccountName   *string `json:"account_name,omitempty" binding:"omitempty,max=255"`
}
