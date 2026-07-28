package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/tutorin-id/tutorin-billing-service/internal/domain"
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
}

type WithdrawalUsecase interface {
	RequestWithdrawal(ctx context.Context, tenantID uuid.UUID, bankAccountID uuid.UUID, amount int64) (*domain.Withdrawal, error)
}
