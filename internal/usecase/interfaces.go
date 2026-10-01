package usecase

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type WalletUsecase interface {
	GetBalance(ctx context.Context, tenantID uuid.UUID) (*domain.WalletBalanceResponse, error)
	// ListLedger returns one tenant's ledger mutations newest first. A tenant
	// without a wallet gets an empty page, not an error.
	ListLedger(ctx context.Context, tenantID uuid.UUID, query domain.LedgerQuery) (*domain.LedgerListResponse, error)
}

type VoucherUsecase interface {
	// List returns one tenant's vouchers newest first with their usage
	// counts.
	List(ctx context.Context, tenantID uuid.UUID, query domain.VoucherQuery) (*domain.VoucherListResponse, error)
	// Get returns one tenant voucher; a foreign id is ErrVoucherNotFound.
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.VoucherResponse, error)
	// Create stores one tenant voucher; a code the tenant already uses is
	// ErrVoucherDuplicate.
	Create(ctx context.Context, tenantID uuid.UUID, req *domain.CreateVoucherRequest) (*domain.VoucherResponse, error)
	// Update patches one tenant voucher; a code another live voucher of
	// the tenant uses is ErrVoucherDuplicate.
	Update(ctx context.Context, tenantID, id uuid.UUID, req *domain.UpdateVoucherRequest) (*domain.VoucherResponse, error)
	// Delete removes a voucher that never discounted a transaction. A
	// voucher with CurrentUses > 0 is ErrVoucherInUse: it can only be
	// deactivated through Update, never deleted.
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
}

type TransactionUsecase interface {
	CreateTransaction(ctx context.Context, tx *domain.Transaction) error
	GetTransaction(ctx context.Context, id uuid.UUID) (*domain.Transaction, error)
	GenerateSubscriptionPayment(ctx context.Context, req *domain.GenerateSubscriptionPaymentRequest) (*domain.GenerateSubscriptionPaymentResponse, error)
	CancelEnrollmentPayment(ctx context.Context, enrollmentID uuid.UUID) (*domain.TransactionResponse, error)
	HandleDuitkuWebhook(ctx context.Context, payload *domain.DuitkuCallbackPayload) error
	List(ctx context.Context, tenantID, parentID *uuid.UUID, query domain.TransactionQuery) (*domain.TransactionListResponse, error)
	SalesSummary(ctx context.Context, tenantID uuid.UUID, from, until time.Time) ([]domain.SalesSummary, error)
	// ExportTransactions streams one tenant's filtered transactions as CSV to
	// w (header plus one row per transaction) and reports the row count. The
	// rows are written in batches, so a 366-day range never loads fully into
	// memory. Query carries the same filters as List; the handler resolves
	// the export defaults (paid status, paid_at basis, last 30 UTC days).
	ExportTransactions(ctx context.Context, tenantID uuid.UUID, query domain.TransactionQuery, w io.Writer) (int64, error)
	GetByIDScoped(ctx context.Context, tenantID, parentID *uuid.UUID, id uuid.UUID) (*domain.TransactionResponse, error)
}

type WithdrawalUsecase interface {
	// RequestWithdrawal moves amount from available to held balance in one
	// transaction and returns the stored request with its frozen destination.
	RequestWithdrawal(ctx context.Context, tenantID uuid.UUID, input domain.RequestWithdrawalInput) (*domain.WithdrawalResponse, error)
	// CancelWithdrawal releases one `requested` withdrawal back to the
	// available balance with a reversal ledger entry.
	CancelWithdrawal(ctx context.Context, tenantID, id uuid.UUID) (*domain.WithdrawalResponse, error)
	// GetWithdrawal returns one tenant withdrawal with its frozen destination.
	GetWithdrawal(ctx context.Context, tenantID, id uuid.UUID) (*domain.WithdrawalResponse, error)
	// ListWithdrawals returns the tenant's withdrawal history newest first.
	ListWithdrawals(ctx context.Context, tenantID uuid.UUID, query domain.WithdrawalQuery) (*domain.WithdrawalListResponse, error)
}

// BankAccountUsecase manages one tenant's payout accounts (KEL-142). Every
// method is scoped to the tenant from the verified token: an id of another
// tenant answers ErrBankAccountNotFound, never a cross-tenant row.
type BankAccountUsecase interface {
	// List returns the tenant's live accounts, primary first, with masked numbers.
	List(ctx context.Context, tenantID uuid.UUID) (*domain.BankAccountListResponse, error)
	// Create stores one account; the first account of a tenant always becomes
	// primary so the exactly-one-primary invariant holds from the start.
	Create(ctx context.Context, tenantID uuid.UUID, req *domain.CreateBankAccountRequest) (*domain.BankAccountResponse, error)
	// Update patches one account; nil fields are left unchanged.
	Update(ctx context.Context, tenantID, id uuid.UUID, req *domain.UpdateBankAccountRequest) (*domain.BankAccountResponse, error)
	// Delete soft-deletes one account. History rows are never removed; a
	// primary still backing an active withdrawal answers ErrBankAccountInUse.
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	// SetPrimary moves the primary slot to id without deleting any row, so
	// payout history survives a primary change.
	SetPrimary(ctx context.Context, tenantID, id uuid.UUID) (*domain.BankAccountResponse, error)
}
