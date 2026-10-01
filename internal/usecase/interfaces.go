package usecase

import (
	"context"
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
	CreateVoucher(ctx context.Context, voucher *domain.Voucher) error
	ValidateVoucher(ctx context.Context, tenantID uuid.UUID, code string, amount int64) (*domain.Voucher, error)
}

type TransactionUsecase interface {
	CreateTransaction(ctx context.Context, tx *domain.Transaction) error
	GetTransaction(ctx context.Context, id uuid.UUID) (*domain.Transaction, error)
	GenerateSubscriptionPayment(ctx context.Context, req *domain.GenerateSubscriptionPaymentRequest) (*domain.GenerateSubscriptionPaymentResponse, error)
	CancelEnrollmentPayment(ctx context.Context, enrollmentID uuid.UUID) (*domain.TransactionResponse, error)
	HandleDuitkuWebhook(ctx context.Context, payload *domain.DuitkuCallbackPayload) error
	List(ctx context.Context, tenantID, parentID *uuid.UUID, query domain.TransactionQuery) (*domain.TransactionListResponse, error)
	SalesSummary(ctx context.Context, tenantID uuid.UUID, from, until time.Time) ([]domain.SalesSummary, error)
	GetByIDScoped(ctx context.Context, tenantID, parentID *uuid.UUID, id uuid.UUID) (*domain.TransactionResponse, error)
}

type WithdrawalUsecase interface {
	RequestWithdrawal(ctx context.Context, tenantID uuid.UUID, bankAccountID uuid.UUID, amount int64) (*domain.Withdrawal, error)
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
