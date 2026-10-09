package usecase

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

type withdrawalUsecase struct {
	wallets     repository.WalletRepository
	ledger      repository.LedgerEntryRepository
	accounts    repository.BankAccountRepository
	withdrawals repository.WithdrawalRepository
	txManager   repository.BillingTransactionManager
	minAmount   int64
}

// NewWithdrawalUsecase serves the tenant withdrawal request and cancel flow
// (KEL-143). A non-positive minAmount means no minimum check; production
// wires the configured WITHDRAWAL_MINIMUM_AMOUNT, which always has a floor.
func NewWithdrawalUsecase(
	wallets repository.WalletRepository,
	ledger repository.LedgerEntryRepository,
	accounts repository.BankAccountRepository,
	withdrawals repository.WithdrawalRepository,
	txManager repository.BillingTransactionManager,
	minAmount int64,
) WithdrawalUsecase {
	return &withdrawalUsecase{
		wallets:     wallets,
		ledger:      ledger,
		accounts:    accounts,
		withdrawals: withdrawals,
		txManager:   txManager,
		minAmount:   minAmount,
	}
}

// RequestWithdrawal moves amount from the tenant's available balance to the
// held balance in one database transaction: wallet row lock, single-open
// guard, primary-account snapshot, withdrawal row, and a `withdrawal_hold`
// ledger entry for -amount. A retry carrying the same idempotency key gets
// the stored row back without moving balance twice.
func (u *withdrawalUsecase) RequestWithdrawal(ctx context.Context, tenantID uuid.UUID, input domain.RequestWithdrawalInput) (*domain.WithdrawalResponse, error) {
	key := strings.TrimSpace(input.IdempotencyKey)
	if input.Amount <= 0 || key == "" || len(key) > 255 {
		return nil, domain.ErrWithdrawalInvalid
	}

	var out *domain.Withdrawal
	err := u.withTransaction(ctx, func(ctx context.Context) error {
		wallet, err := u.lockWallet(ctx, tenantID)
		if err != nil {
			return err
		}
		if wallet == nil {
			// No wallet means no credited payment ever arrived, so any
			// positive amount exceeds the zero available balance.
			return domain.ErrWithdrawalInsufficientBalance
		}
		existing, err := u.withdrawals.GetByTenantAndKey(ctx, tenantID, key)
		if err == nil {
			if existing.Amount != input.Amount || (input.BankAccountID != nil && existing.BankAccountID != *input.BankAccountID) {
				return domain.ErrWithdrawalIdempotencyMismatch
			}
			out = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if u.minAmount > 0 && input.Amount < u.minAmount {
			return domain.ErrWithdrawalBelowMinimum
		}
		open, err := u.withdrawals.CountOpenByTenant(ctx, tenantID)
		if err != nil {
			return err
		}
		if open > 0 {
			return domain.ErrWithdrawalOpenExists
		}
		if input.Amount > wallet.AvailableBalance {
			return domain.ErrWithdrawalInsufficientBalance
		}
		account, err := u.resolveDestination(ctx, tenantID, input.BankAccountID)
		if err != nil {
			return err
		}
		if wallet.PendingBalance < 0 || wallet.PendingBalance > math.MaxInt64-input.Amount {
			return fmt.Errorf("invalid withdrawal hold state for tenant %s", tenantID)
		}
		now := time.Now().UTC()
		wallet.AvailableBalance -= input.Amount
		wallet.PendingBalance += input.Amount
		if err := u.wallets.Update(ctx, wallet); err != nil {
			return err
		}
		created := &domain.Withdrawal{
			ID:                    uuid.New(),
			TenantID:              tenantID,
			BankAccountID:         account.ID,
			Amount:                input.Amount,
			AdminFee:              0,
			NetAmount:             input.Amount,
			Status:                domain.WithdrawalStatusRequested,
			RequestedAt:           now,
			BankCodeSnapshot:      account.BankCode,
			AccountNumberSnapshot: account.AccountNumber,
			AccountNameSnapshot:   account.AccountName,
		}
		created.IdempotencyKey = &key
		if err := u.withdrawals.Create(ctx, created); err != nil {
			return err
		}
		desc := fmt.Sprintf("Withdrawal hold for request %s", created.ID)
		if err := u.ledger.Create(ctx, &domain.LedgerEntry{
			ID:            uuid.New(),
			WalletID:      wallet.ID,
			ReferenceID:   created.ID,
			ReferenceType: domain.WithdrawalReferenceType,
			Amount:        -input.Amount,
			EntryType:     domain.LedgerEntryTypeHold,
			Description:   &desc,
		}); err != nil {
			return fmt.Errorf("failed to create withdrawal hold ledger entry: %w", err)
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return domain.ToWithdrawalResponse(out), nil
}

// resolveDestination returns the payout account: the explicit one when set,
// the tenant's primary otherwise. The returned row is the snapshot source.
func (u *withdrawalUsecase) resolveDestination(ctx context.Context, tenantID uuid.UUID, bankAccountID *uuid.UUID) (*domain.BankAccount, error) {
	if bankAccountID != nil {
		if *bankAccountID == uuid.Nil {
			return nil, domain.ErrWithdrawalInvalid
		}
		account, err := u.accounts.GetByIDScopedForUpdate(ctx, tenantID, *bankAccountID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, domain.ErrWithdrawalNotFound
			}
			return nil, err
		}
		if !account.IsPrimary {
			return nil, domain.ErrWithdrawalNoPrimaryAccount
		}
		return account, nil
	}
	accounts, err := u.accounts.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		if accounts[i].IsPrimary {
			// Lock the selected row before copying the destination fields;
			// a concurrent account edit/primary switch then cannot race the
			// snapshot. The account is re-checked after the lock.
			account, err := u.accounts.GetByIDScopedForUpdate(ctx, tenantID, accounts[i].ID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, domain.ErrWithdrawalNoPrimaryAccount
				}
				return nil, err
			}
			if !account.IsPrimary {
				return nil, domain.ErrWithdrawalNoPrimaryAccount
			}
			return account, nil
		}
	}
	return nil, domain.ErrWithdrawalNoPrimaryAccount
}

// CancelWithdrawal releases one `requested` withdrawal back to the available
// balance with a `withdrawal_release` ledger entry for +amount. The status
// move is one conditional statement, so of any number of concurrent
// cancellers (or a cancel racing a future admin processing step) exactly one
// wins; the losers see the winner's state as 409, and a foreign or missing
// id as 404.
func (u *withdrawalUsecase) CancelWithdrawal(ctx context.Context, tenantID, id uuid.UUID) (*domain.WithdrawalResponse, error) {
	if id == uuid.Nil {
		return nil, domain.ErrWithdrawalInvalid
	}
	var out *domain.Withdrawal
	err := u.withTransaction(ctx, func(ctx context.Context) error {
		wallet, err := u.lockWallet(ctx, tenantID)
		if err != nil {
			return err
		}
		if wallet == nil {
			return domain.ErrWithdrawalNotFound
		}
		claimed, err := u.withdrawals.ClaimCancelled(ctx, tenantID, id, time.Now().UTC())
		if err != nil {
			return err
		}
		if !claimed {
			return u.cancelLoserError(ctx, tenantID, id)
		}
		cancelled, err := u.withdrawals.GetByIDScoped(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if cancelled.Amount <= 0 || wallet.PendingBalance < cancelled.Amount || wallet.AvailableBalance > math.MaxInt64-cancelled.Amount {
			return fmt.Errorf("invalid withdrawal hold state for %s", cancelled.ID)
		}
		wallet.AvailableBalance += cancelled.Amount
		wallet.PendingBalance -= cancelled.Amount
		if err := u.wallets.Update(ctx, wallet); err != nil {
			return err
		}
		desc := fmt.Sprintf("Withdrawal release for cancelled request %s", cancelled.ID)
		if err := u.ledger.Create(ctx, &domain.LedgerEntry{
			ID:            uuid.New(),
			WalletID:      wallet.ID,
			ReferenceID:   cancelled.ID,
			ReferenceType: domain.WithdrawalReferenceType,
			Amount:        cancelled.Amount,
			EntryType:     domain.LedgerEntryTypeRelease,
			Description:   &desc,
		}); err != nil {
			return fmt.Errorf("failed to create withdrawal release ledger entry: %w", err)
		}
		out = cancelled
		return nil
	})
	if err != nil {
		return nil, err
	}
	return domain.ToWithdrawalResponse(out), nil
}

// cancelLoserError maps a lost cancel claim: foreign or missing ids are 404,
// anything else is the 409 the winner's state dictates.
func (u *withdrawalUsecase) cancelLoserError(ctx context.Context, tenantID, id uuid.UUID) error {
	existing, err := u.withdrawals.GetByIDScoped(ctx, tenantID, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrWithdrawalNotFound
		}
		return err
	}
	_ = existing
	return domain.ErrWithdrawalInvalidState
}

// GetWithdrawal returns one tenant withdrawal with its frozen destination.
func (u *withdrawalUsecase) GetWithdrawal(ctx context.Context, tenantID, id uuid.UUID) (*domain.WithdrawalResponse, error) {
	if id == uuid.Nil {
		return nil, domain.ErrWithdrawalInvalid
	}
	w, err := u.withdrawals.GetByIDScoped(ctx, tenantID, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrWithdrawalNotFound
		}
		return nil, err
	}
	return domain.ToWithdrawalResponse(w), nil
}

// ListWithdrawals returns the tenant's withdrawal history newest first.
func (u *withdrawalUsecase) ListWithdrawals(ctx context.Context, tenantID uuid.UUID, query domain.WithdrawalQuery) (*domain.WithdrawalListResponse, error) {
	page, pageSize := query.Page, query.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	rows, total, err := u.withdrawals.ListByTenant(ctx, tenantID, page, pageSize)
	if err != nil {
		return nil, err
	}
	items := make([]domain.WithdrawalResponse, 0, len(rows))
	for i := range rows {
		items = append(items, *domain.ToWithdrawalResponse(&rows[i]))
	}
	out := &domain.WithdrawalListResponse{Items: items}
	out.Pagination.Page = page
	out.Pagination.PageSize = pageSize
	out.Pagination.TotalItems = total
	out.Pagination.TotalPages = int(math.Ceil(float64(total) / float64(pageSize)))
	return out, nil
}

// lockWallet takes the tenant's wallet row exclusively inside the caller's
// transaction, so concurrent requests and cancels serialize on it. It
// returns (nil, nil) when the tenant never received a credited payment. When
// no transaction manager is wired (unit tests) it runs without a transaction.
func (u *withdrawalUsecase) lockWallet(ctx context.Context, tenantID uuid.UUID) (*domain.Wallet, error) {
	if locking, ok := u.wallets.(repository.WalletLockingRepository); ok {
		wallet, err := locking.GetByTenantIDForUpdate(ctx, tenantID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return wallet, nil
	}
	wallet, err := u.wallets.GetByTenantID(ctx, tenantID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return wallet, nil
}

func (u *withdrawalUsecase) withTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if u.txManager == nil {
		return fn(ctx)
	}
	return u.txManager.WithTransaction(ctx, fn)
}
