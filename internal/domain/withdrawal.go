package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrWithdrawalInvalid answers 400: the payload shape itself is wrong
	// (non-positive amount, malformed account id, missing withdrawal id).
	ErrWithdrawalInvalid = errors.New("invalid withdrawal request")
	// ErrWithdrawalBelowMinimum answers 422: the amount is below the
	// configured minimum.
	ErrWithdrawalBelowMinimum = errors.New("withdrawal amount below minimum")
	// ErrWithdrawalInsufficientBalance answers 422: the amount exceeds the
	// tenant's available balance.
	ErrWithdrawalInsufficientBalance = errors.New("insufficient available balance")
	// ErrWithdrawalNoPrimaryAccount answers 422: the tenant has no primary
	// payout account to pay out to.
	ErrWithdrawalNoPrimaryAccount = errors.New("tenant has no primary bank account")
	// ErrWithdrawalOpenExists answers 409: the tenant already has one open
	// request; only one open request per tenant is allowed.
	ErrWithdrawalOpenExists = errors.New("an open withdrawal request already exists")
	// ErrWithdrawalIdempotencyMismatch answers 409: the idempotency key was
	// already used for a request with a different amount.
	ErrWithdrawalIdempotencyMismatch = errors.New("idempotency key already used with a different request")
	// ErrWithdrawalNotFound answers 404: the withdrawal does not exist or
	// belongs to another tenant. One value for both so a caller cannot probe
	// which tenants own which withdrawals.
	ErrWithdrawalNotFound = errors.New("withdrawal not found")
	// ErrWithdrawalInvalidState answers 409: the withdrawal is no longer in
	// `requested`, so the tenant cancel no longer applies.
	ErrWithdrawalInvalidState       = errors.New("withdrawal cannot be cancelled in its current status")
	ErrWithdrawalDuplicateReference = errors.New("transfer reference already used")
)

// Withdrawal statuses. `requested` and `processing` are the open states in
// WithdrawalActiveStatuses: they still hold balance and still reference their
// destination account. `cancelled`, `completed` and `failed` are terminal.
// Admin processing (requested -> processing -> completed/failed) is out of
// scope for KEL-143; the states exist so a later step can move them without a
// schema change.
const (
	WithdrawalStatusRequested  = "requested"
	WithdrawalStatusProcessing = "processing"
	WithdrawalStatusCancelled  = "cancelled"
	WithdrawalStatusCompleted  = "completed"
	WithdrawalStatusFailed     = "failed"
	WithdrawalStatusPaid       = "paid"
	WithdrawalStatusRejected   = "rejected"
)

// Ledger evidence for withdrawals. A request holds -amount; cancel/reject
// releases +amount. Paying releases +amount and records an external outflow
// of -amount. The signed sum remains the available balance, while the open
// requested/processing rows equal the held balance.
const (
	WithdrawalReferenceType = "withdrawal"
	LedgerEntryTypeHold     = "withdrawal_hold"
	LedgerEntryTypeRelease  = "withdrawal_release"
	LedgerEntryTypePayment  = "payment_received"
	LedgerEntryTypePaid     = "withdrawal_paid"
)

type Withdrawal struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID         uuid.UUID  `gorm:"type:uuid;not null;index" json:"tenant_id"` // Cross-service
	BankAccountID    uuid.UUID  `gorm:"type:uuid;not null;index" json:"bank_account_id"`
	Amount           int64      `gorm:"type:bigint;not null" json:"amount"`
	AdminFee         int64      `gorm:"type:bigint;not null;default:0" json:"admin_fee"`
	NetAmount        int64      `gorm:"type:bigint;not null" json:"net_amount"`
	Status           string     `gorm:"type:varchar(255);not null" json:"status"`
	ProviderPayoutID *string    `gorm:"type:varchar(255);unique;index" json:"provider_payout_id,omitempty"`
	RequestedAt      time.Time  `gorm:"type:timestamp;not null;default:now()" json:"requested_at"`
	ProcessedAt      *time.Time `gorm:"type:timestamp" json:"processed_at,omitempty"`
	// IdempotencyKey is the client retry key scoped per tenant. NULL for rows
	// written before KEL-143, which predate the retry contract.
	IdempotencyKey *string `gorm:"type:varchar(255)" json:"-"`
	// BankCodeSnapshot, AccountNumberSnapshot and AccountNameSnapshot freeze
	// the payout destination at request time, so a later primary change or
	// account edit never rewrites where an open request pays out.
	BankCodeSnapshot      string     `gorm:"type:varchar(255);not null;default:''" json:"-"`
	AccountNumberSnapshot string     `gorm:"type:varchar(255);not null;default:''" json:"-"`
	AccountNameSnapshot   string     `gorm:"type:varchar(255);not null;default:''" json:"-"`
	CancelledAt           *time.Time `gorm:"type:timestamp" json:"cancelled_at,omitempty"`
	DecidedBy             *uuid.UUID `gorm:"type:uuid" json:"decided_by,omitempty"`
	DecidedAt             *time.Time `gorm:"type:timestamp" json:"decided_at,omitempty"`
	TransferReference     *string    `gorm:"type:varchar(255)" json:"transfer_reference,omitempty"`
	RejectReason          *string    `gorm:"type:text" json:"reject_reason,omitempty"`

	BankAccount *BankAccount `gorm:"foreignKey:BankAccountID" json:"bank_account,omitempty"`
}

// RequestWithdrawalInput is the validated tenant intent to move amount from
// available to held balance. A nil BankAccountID pays out to the tenant's
// current primary account; a set one must be that tenant's primary account.
// IdempotencyKey is required to make retries safe.
type RequestWithdrawalInput struct {
	BankAccountID  *uuid.UUID
	Amount         int64
	IdempotencyKey string
}

// RequestWithdrawalRequest is the tenant HTTP payload (KEL-143). The bank
// account is optional: omitted means the current primary account.
type RequestWithdrawalRequest struct {
	BankAccountID  string `json:"bank_account_id,omitempty"`
	Amount         int64  `json:"amount" binding:"required"`
	IdempotencyKey string `json:"idempotency_key" binding:"required,max=255"`
}

// WithdrawalQuery carries the history pagination the handler parsed.
type WithdrawalQuery struct {
	Page     int
	PageSize int
}

// WithdrawalResponse is one tenant-visible withdrawal. The destination
// account number is always masked like the bank-account list, so a leaked
// response never exposes a full account number.
type WithdrawalResponse struct {
	ID                uuid.UUID  `json:"id"`
	Amount            int64      `json:"amount"`
	AdminFee          int64      `json:"admin_fee"`
	NetAmount         int64      `json:"net_amount"`
	Status            string     `json:"status"`
	BankCode          string     `json:"bank_code"`
	AccountNumber     string     `json:"account_number"`
	AccountName       string     `json:"account_name"`
	RequestedAt       time.Time  `json:"requested_at"`
	ProcessedAt       *time.Time `json:"processed_at,omitempty"`
	CancelledAt       *time.Time `json:"cancelled_at,omitempty"`
	DecidedBy         *uuid.UUID `json:"decided_by,omitempty"`
	DecidedAt         *time.Time `json:"decided_at,omitempty"`
	TransferReference *string    `json:"transfer_reference,omitempty"`
	RejectReason      *string    `json:"reject_reason,omitempty"`
}

// PlatformWithdrawalResponse is deliberately separate: tenant responses always mask
// account numbers, whereas an authorized operator needs the frozen full destination.
type PlatformWithdrawalResponse struct {
	WithdrawalResponse
	TenantID uuid.UUID `json:"tenant_id"`
}

func ToPlatformWithdrawalResponse(w *Withdrawal) *PlatformWithdrawalResponse {
	out := ToWithdrawalResponse(w)
	out.AccountNumber = w.AccountNumberSnapshot
	return &PlatformWithdrawalResponse{WithdrawalResponse: *out, TenantID: w.TenantID}
}

type PlatformWithdrawalListResponse struct {
	Items      []PlatformWithdrawalResponse `json:"items"`
	Pagination struct {
		Page       int   `json:"page"`
		PageSize   int   `json:"page_size"`
		TotalItems int64 `json:"total_items"`
		TotalPages int   `json:"total_pages"`
	} `json:"pagination"`
}

type MarkWithdrawalPaidRequest struct {
	TransferReference string `json:"transfer_reference" binding:"required,max=255"`
}
type RejectWithdrawalRequest struct {
	Reason string `json:"reason" binding:"required,max=2000"`
}

// WithdrawalListResponse mirrors LedgerListResponse pagination.
type WithdrawalListResponse struct {
	Items      []WithdrawalResponse `json:"items"`
	Pagination struct {
		Page       int   `json:"page"`
		PageSize   int   `json:"page_size"`
		TotalItems int64 `json:"total_items"`
		TotalPages int   `json:"total_pages"`
	} `json:"pagination"`
}

func ToWithdrawalResponse(w *Withdrawal) *WithdrawalResponse {
	return &WithdrawalResponse{
		ID:                w.ID,
		Amount:            w.Amount,
		AdminFee:          w.AdminFee,
		NetAmount:         w.NetAmount,
		Status:            w.Status,
		BankCode:          w.BankCodeSnapshot,
		AccountNumber:     MaskAccountNumber(w.AccountNumberSnapshot),
		AccountName:       w.AccountNameSnapshot,
		RequestedAt:       w.RequestedAt,
		ProcessedAt:       w.ProcessedAt,
		CancelledAt:       w.CancelledAt,
		DecidedBy:         w.DecidedBy,
		DecidedAt:         w.DecidedAt,
		TransferReference: w.TransferReference,
		RejectReason:      w.RejectReason,
	}
}

// WalletLedgerInvariant reports whether the wallet row matches its ledger
// evidence on withdrawal-aware data: the available balance equals the signed
// ledger sum (payments in, holds out, releases back) and the held balance
// equals the total still frozen by open requests.
func WalletLedgerInvariant(wallet *Wallet, ledgerSum, openHoldTotal int64) bool {
	return wallet.AvailableBalance == ledgerSum && wallet.PendingBalance == openHoldTotal
}
