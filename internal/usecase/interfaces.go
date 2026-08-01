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
	HandleFlipWebhook(ctx context.Context, payload *domain.FlipWebhookPayload, validationToken string) error
}

type WithdrawalUsecase interface {
	RequestWithdrawal(ctx context.Context, tenantID uuid.UUID, bankAccountID uuid.UUID, amount int64) (*domain.Withdrawal, error)
}
