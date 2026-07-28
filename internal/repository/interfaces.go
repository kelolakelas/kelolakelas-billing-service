package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/tutorin-id/tutorin-billing-service/internal/domain"
)

type WalletRepository interface {
	GetByTenantID(ctx context.Context, tenantID uuid.UUID) (*domain.Wallet, error)
	Update(ctx context.Context, wallet *domain.Wallet) error
}

type LedgerEntryRepository interface {
	Create(ctx context.Context, entry *domain.LedgerEntry) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.LedgerEntry, error)
}

type BankAccountRepository interface {
	Create(ctx context.Context, account *domain.BankAccount) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.BankAccount, error)
	Update(ctx context.Context, account *domain.BankAccount) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type WithdrawalRepository interface {
	Create(ctx context.Context, withdrawal *domain.Withdrawal) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Withdrawal, error)
	Update(ctx context.Context, withdrawal *domain.Withdrawal) error
}

type VoucherRepository interface {
	Create(ctx context.Context, voucher *domain.Voucher) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Voucher, error)
	GetByCode(ctx context.Context, tenantID uuid.UUID, code string) (*domain.Voucher, error)
	Update(ctx context.Context, voucher *domain.Voucher) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type TransactionRepository interface {
	Create(ctx context.Context, transaction *domain.Transaction) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Transaction, error)
	Update(ctx context.Context, transaction *domain.Transaction) error
}
