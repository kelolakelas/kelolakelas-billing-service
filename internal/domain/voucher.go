package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	// ErrVoucherNotFound answers 404: the voucher does not exist or belongs
	// to another tenant. One value for both so a caller cannot probe which
	// tenants own which vouchers.
	ErrVoucherNotFound = errors.New("voucher not found")
	// ErrVoucherInvalid answers 400 for malformed payloads and rule
	// violations (bad code, discount value, date range, or usage cap).
	ErrVoucherInvalid = errors.New("invalid voucher request")
	// ErrVoucherDuplicate answers 409: another live voucher of the same
	// tenant already uses the code (compared case-insensitively).
	ErrVoucherDuplicate = errors.New("voucher code already exists")
	// ErrVoucherInUse answers 409: the voucher already discounted a
	// transaction, so it can only be deactivated, never deleted.
	ErrVoucherInUse = errors.New("voucher has been used and can only be deactivated")
)

// Voucher discount types. `percentage` takes DiscountValue as 1-100;
// `fixed_amount` takes DiscountValue as a positive IDR nominal.
const (
	VoucherDiscountPercentage  = "percentage"
	VoucherDiscountFixedAmount = "fixed_amount"
)

type Voucher struct {
	ID                   uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID             uuid.UUID      `gorm:"type:uuid;not null;index" json:"tenant_id"` // Cross-service
	Code                 string         `gorm:"type:varchar(255);not null" json:"code"`
	DiscountType         string         `gorm:"type:varchar(255);not null" json:"discount_type"`
	DiscountValue        float64        `gorm:"type:numeric(15,2);not null" json:"discount_value"`
	MaxDiscountAmount    *int64         `gorm:"type:bigint" json:"max_discount_amount,omitempty"`
	MinTransactionAmount int64          `gorm:"type:bigint;not null;default:0" json:"min_transaction_amount"`
	MaxUses              *int           `gorm:"type:integer" json:"max_uses,omitempty"`
	CurrentUses          int            `gorm:"type:integer;not null;default:0" json:"current_uses"`
	ValidFrom            *time.Time     `gorm:"type:timestamp" json:"valid_from,omitempty"`
	ValidUntil           *time.Time     `gorm:"type:timestamp" json:"valid_until,omitempty"`
	IsActive             bool           `gorm:"type:boolean;not null;default:true;index" json:"is_active"`
	CreatedAt            time.Time      `gorm:"type:timestamp;not null;default:now()" json:"created_at"`
	UpdatedAt            time.Time      `gorm:"type:timestamp;not null;default:now()" json:"updated_at"`
	DeletedAt            gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

// NormalizeVoucherCode canonicalizes a voucher code for storage and
// comparison. Codes are unique case-insensitively per tenant (KEL-161), and
// the database constraint is an exact match, so new code writes store the
// canonical form: trimmed and uppercased. Existing mixed-case rows remain
// valid; the write path checks for them under a tenant-scoped transaction lock.
func NormalizeVoucherCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// ValidateVoucherFields checks the shared create/update semantics: a
// percentage is 1-100, a nominal is positive, the date range is ordered, and
// the usage cap never drops below what was already consumed.
func ValidateVoucherFields(code, discountType string, discountValue float64, minTransactionAmount int64, maxDiscountAmount *int64, maxUses *int, currentUses int, validFrom, validUntil *time.Time) error {
	if strings.TrimSpace(code) == "" || len([]rune(code)) > 255 {
		return ErrVoucherInvalid
	}
	switch discountType {
	case VoucherDiscountPercentage:
		if discountValue < 1 || discountValue > 100 {
			return ErrVoucherInvalid
		}
	case VoucherDiscountFixedAmount:
		if discountValue <= 0 {
			return ErrVoucherInvalid
		}
	default:
		return ErrVoucherInvalid
	}
	if minTransactionAmount < 0 {
		return ErrVoucherInvalid
	}
	if maxDiscountAmount != nil && *maxDiscountAmount <= 0 {
		return ErrVoucherInvalid
	}
	if currentUses < 0 {
		return ErrVoucherInvalid
	}
	if maxUses != nil && (*maxUses <= 0 || *maxUses < currentUses) {
		return ErrVoucherInvalid
	}
	if validFrom != nil && validUntil != nil && validUntil.Before(*validFrom) {
		return ErrVoucherInvalid
	}
	return nil
}

// CreateVoucherRequest is the tenant write payload (KEL-161). IsActive is a
// pointer so omission means active; checkout redemption (out of scope) only
// ever honours active, in-window vouchers.
type CreateVoucherRequest struct {
	Code                 string     `json:"code" binding:"required,max=255"`
	DiscountType         string     `json:"discount_type" binding:"required"`
	DiscountValue        float64    `json:"discount_value" binding:"required"`
	MaxDiscountAmount    *int64     `json:"max_discount_amount,omitempty"`
	MinTransactionAmount int64      `json:"min_transaction_amount"`
	MaxUses              *int       `json:"max_uses,omitempty"`
	ValidFrom            *time.Time `json:"valid_from,omitempty"`
	ValidUntil           *time.Time `json:"valid_until,omitempty"`
	IsActive             *bool      `json:"is_active,omitempty"`
}

// UpdateVoucherRequest patches one voucher. Nil fields are left unchanged;
// reactivation of an expired voucher is allowed, so no validity check runs
// on the IsActive toggle.
type UpdateVoucherRequest struct {
	Code                 *string    `json:"code,omitempty" binding:"omitempty,max=255"`
	DiscountType         *string    `json:"discount_type,omitempty"`
	DiscountValue        *float64   `json:"discount_value,omitempty"`
	MaxDiscountAmount    *int64     `json:"max_discount_amount,omitempty"`
	MinTransactionAmount *int64     `json:"min_transaction_amount,omitempty"`
	MaxUses              *int       `json:"max_uses,omitempty"`
	ValidFrom            *time.Time `json:"valid_from,omitempty"`
	ValidUntil           *time.Time `json:"valid_until,omitempty"`
	IsActive             *bool      `json:"is_active,omitempty"`
}

// VoucherResponse is what a tenant member reads (KEL-161), including the
// usage count the list screen shows next to every row.
type VoucherResponse struct {
	ID                   uuid.UUID  `json:"id"`
	Code                 string     `json:"code"`
	DiscountType         string     `json:"discount_type"`
	DiscountValue        float64    `json:"discount_value"`
	MaxDiscountAmount    *int64     `json:"max_discount_amount,omitempty"`
	MinTransactionAmount int64      `json:"min_transaction_amount"`
	MaxUses              *int       `json:"max_uses,omitempty"`
	CurrentUses          int        `json:"current_uses"`
	ValidFrom            *time.Time `json:"valid_from,omitempty"`
	ValidUntil           *time.Time `json:"valid_until,omitempty"`
	IsActive             bool       `json:"is_active"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func ToVoucherResponse(voucher *Voucher) *VoucherResponse {
	return &VoucherResponse{
		ID:                   voucher.ID,
		Code:                 voucher.Code,
		DiscountType:         voucher.DiscountType,
		DiscountValue:        voucher.DiscountValue,
		MaxDiscountAmount:    voucher.MaxDiscountAmount,
		MinTransactionAmount: voucher.MinTransactionAmount,
		MaxUses:              voucher.MaxUses,
		CurrentUses:          voucher.CurrentUses,
		ValidFrom:            voucher.ValidFrom,
		ValidUntil:           voucher.ValidUntil,
		IsActive:             voucher.IsActive,
		CreatedAt:            voucher.CreatedAt,
		UpdatedAt:            voucher.UpdatedAt,
	}
}

// VoucherQuery carries the list pagination the handler parses.
type VoucherQuery struct {
	Page     int
	PageSize int
}

// VoucherListResponse mirrors LedgerListResponse pagination.
type VoucherListResponse struct {
	Items      []VoucherResponse `json:"items"`
	Pagination struct {
		Page       int   `json:"page"`
		PageSize   int   `json:"page_size"`
		TotalItems int64 `json:"total_items"`
		TotalPages int   `json:"total_pages"`
	} `json:"pagination"`
}
