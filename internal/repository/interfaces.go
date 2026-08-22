package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type WalletRepository interface {
	GetByTenantID(ctx context.Context, tenantID uuid.UUID) (*domain.Wallet, error)
	Create(ctx context.Context, wallet *domain.Wallet) error
	Update(ctx context.Context, wallet *domain.Wallet) error
}

type WalletLockingRepository interface {
	GetByTenantIDForUpdate(ctx context.Context, tenantID uuid.UUID) (*domain.Wallet, error)
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
	GetByMerchantOrderID(ctx context.Context, merchantOrderID string) (*domain.Transaction, error)
	GetByEnrollmentID(ctx context.Context, enrollmentID uuid.UUID) (*domain.Transaction, error)
	GetBySubscriptionPeriod(ctx context.Context, subscriptionID uuid.UUID, period time.Time) (*domain.Transaction, error)
	GetByPaymentIntentID(ctx context.Context, paymentIntentID string) (*domain.Transaction, error)
	List(ctx context.Context, tenantID *uuid.UUID, parentID *uuid.UUID, query domain.TransactionQuery) ([]domain.Transaction, int64, error)
	Update(ctx context.Context, transaction *domain.Transaction) error
}

type TransactionLockingRepository interface {
	GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Transaction, error)
	ClaimInvoice(ctx context.Context, id uuid.UUID) (bool, error)
	ClaimPaymentLinkEmail(ctx context.Context, id uuid.UUID, sentAt time.Time) (bool, error)
	ClaimReminderEmail(ctx context.Context, id uuid.UUID, sentAt time.Time, intervalDays int) (bool, error)
}

type BillingTransactionManager interface {
	WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

type SubscriptionRepository interface {
	Create(ctx context.Context, subscription *domain.Subscription) error
	GetByEnrollmentID(ctx context.Context, enrollmentID uuid.UUID) (*domain.Subscription, error)
	Update(ctx context.Context, subscription *domain.Subscription) error
	ListDueForRenewal(ctx context.Context, before time.Time) ([]domain.Subscription, error)
}
