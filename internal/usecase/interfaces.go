package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type WalletUsecase interface {
	GetBalance(ctx context.Context, tenantID uuid.UUID) (*domain.Wallet, error)
}

type VoucherUsecase interface {
	CreateVoucher(ctx context.Context, voucher *domain.Voucher) error
	ValidateVoucher(ctx context.Context, tenantID uuid.UUID, code string, amount int64) (*domain.Voucher, error)
}

type TransactionUsecase interface {
	CreateTransaction(ctx context.Context, tx *domain.Transaction) error
	GetTransaction(ctx context.Context, id uuid.UUID) (*domain.Transaction, error)
	GenerateSubscriptionPayment(ctx context.Context, req *domain.GenerateSubscriptionPaymentRequest) (*domain.GenerateSubscriptionPaymentResponse, error)
	HandleDuitkuWebhook(ctx context.Context, payload *domain.DuitkuCallbackPayload) error
	List(ctx context.Context, tenantID, parentID *uuid.UUID, query domain.TransactionQuery) (*domain.TransactionListResponse, error)
	GetByIDScoped(ctx context.Context, tenantID, parentID *uuid.UUID, id uuid.UUID) (*domain.TransactionResponse, error)
}

type WithdrawalUsecase interface {
	RequestWithdrawal(ctx context.Context, tenantID uuid.UUID, bankAccountID uuid.UUID, amount int64) (*domain.Withdrawal, error)
}
