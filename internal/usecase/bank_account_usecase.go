package usecase

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

type bankAccountUsecase struct {
	accounts    repository.BankAccountRepository
	withdrawals repository.WithdrawalRepository
	txManager   repository.BillingTransactionManager
}

// NewBankAccountUsecase serves the tenant payout-account management (KEL-142).
// withdrawals may be nil only in tests that never delete a primary account;
// production always wires the real repository.
func NewBankAccountUsecase(accounts repository.BankAccountRepository, withdrawals repository.WithdrawalRepository, txManager repository.BillingTransactionManager) BankAccountUsecase {
	return &bankAccountUsecase{accounts: accounts, withdrawals: withdrawals, txManager: txManager}
}

func validateBankAccountField(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len([]rune(trimmed)) <= 255
}

func (u *bankAccountUsecase) List(ctx context.Context, tenantID uuid.UUID) (*domain.BankAccountListResponse, error) {
	accounts, err := u.accounts.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	items := make([]domain.BankAccountResponse, 0, len(accounts))
	for i := range accounts {
		items = append(items, *domain.ToBankAccountResponse(&accounts[i]))
	}
	return &domain.BankAccountListResponse{Items: items}, nil
}

func (u *bankAccountUsecase) Create(ctx context.Context, tenantID uuid.UUID, req *domain.CreateBankAccountRequest) (*domain.BankAccountResponse, error) {
	if req == nil || !validateBankAccountField(req.BankCode) || !validateBankAccountField(req.AccountNumber) || !validateBankAccountField(req.AccountName) {
		return nil, domain.ErrBankAccountInvalid
	}
	hasPrimary, err := u.accounts.HasPrimary(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	// The first account of a tenant always becomes primary, even when the
	// caller did not ask for it, so the at-most-one-primary invariant holds
	// from the start. Later accounts default to non-primary.
	wantPrimary := !hasPrimary
	if req.IsPrimary != nil {
		wantPrimary = *req.IsPrimary || !hasPrimary
	}
	account := &domain.BankAccount{
		ID:            uuid.New(),
		TenantID:      tenantID,
		BankCode:      strings.TrimSpace(req.BankCode),
		AccountNumber: strings.TrimSpace(req.AccountNumber),
		AccountName:   strings.TrimSpace(req.AccountName),
		IsPrimary:     wantPrimary,
	}
	create := func(ctx context.Context) error {
		if wantPrimary {
			if err := u.accounts.ClearPrimary(ctx, tenantID); err != nil {
				return err
			}
		}
		return u.accounts.Create(ctx, account)
	}
	if u.txManager != nil && wantPrimary {
		if err := u.txManager.WithTransaction(ctx, create); err != nil {
			return nil, mapBankAccountWriteError(err)
		}
	} else if err := create(ctx); err != nil {
		return nil, mapBankAccountWriteError(err)
	}
	return domain.ToBankAccountResponse(account), nil
}

func (u *bankAccountUsecase) Update(ctx context.Context, tenantID, id uuid.UUID, req *domain.UpdateBankAccountRequest) (*domain.BankAccountResponse, error) {
	if req == nil || (req.BankCode == nil && req.AccountNumber == nil && req.AccountName == nil) {
		return nil, domain.ErrBankAccountInvalid
	}
	// Validate before locking so malformed payloads do not acquire a row
	// lock. The transaction and row lock serialize an edit with a withdrawal
	// snapshot on the same destination.
	for _, field := range []*string{req.BankCode, req.AccountNumber, req.AccountName} {
		if field != nil && !validateBankAccountField(*field) {
			return nil, domain.ErrBankAccountInvalid
		}
	}
	var account *domain.BankAccount
	update := func(ctx context.Context) error {
		var err error
		account, err = u.accounts.GetByIDScopedForUpdate(ctx, tenantID, id)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrBankAccountNotFound
			}
			return err
		}
		if req.BankCode != nil {
			account.BankCode = strings.TrimSpace(*req.BankCode)
		}
		if req.AccountNumber != nil {
			account.AccountNumber = strings.TrimSpace(*req.AccountNumber)
		}
		if req.AccountName != nil {
			account.AccountName = strings.TrimSpace(*req.AccountName)
		}
		return u.accounts.Update(ctx, account)
	}
	if u.txManager != nil {
		if err := u.txManager.WithTransaction(ctx, update); err != nil {
			return nil, mapBankAccountWriteError(err)
		}
	} else if err := update(ctx); err != nil {
		return nil, mapBankAccountWriteError(err)
	}
	return domain.ToBankAccountResponse(account), nil
}

func (u *bankAccountUsecase) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	remove := func(ctx context.Context) error {
		// Lock the destination row before checking active withdrawals. A
		// concurrent request locks the same row before taking its snapshot,
		// so a deletion cannot pass the check then delete a newly active
		// destination while the request commits.
		account, err := u.accounts.GetByIDScopedForUpdate(ctx, tenantID, id)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrBankAccountNotFound
			}
			return err
		}
		if u.withdrawals == nil {
			if account.IsPrimary {
				return domain.ErrBankAccountInUse
			}
		} else {
			count, err := u.withdrawals.CountActiveByBankAccount(ctx, account.ID, domain.WithdrawalActiveStatuses)
			if err != nil {
				return err
			}
			if count > 0 {
				return domain.ErrBankAccountInUse
			}
		}
		deleted, err := u.accounts.SoftDelete(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if !deleted {
			return domain.ErrBankAccountNotFound
		}
		return nil
	}
	if u.txManager != nil {
		return u.txManager.WithTransaction(ctx, remove)
	}
	return remove(ctx)
}

// SetPrimary moves the primary slot to id without deleting any row, so payout
// history survives a primary change. The target row is locked first, inside a
// transaction when one is available; the partial unique index is the backstop
// that serializes concurrent promoters.
func (u *bankAccountUsecase) SetPrimary(ctx context.Context, tenantID, id uuid.UUID) (*domain.BankAccountResponse, error) {
	promote := func(ctx context.Context) error {
		if _, err := u.accounts.GetByIDScopedForUpdate(ctx, tenantID, id); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrBankAccountNotFound
			}
			return err
		}
		return u.accounts.SetPrimary(ctx, tenantID, id)
	}
	if u.txManager != nil {
		if err := u.txManager.WithTransaction(ctx, promote); err != nil {
			return nil, mapBankAccountWriteError(err)
		}
	} else if err := promote(ctx); err != nil {
		return nil, mapBankAccountWriteError(err)
	}
	account, err := u.accounts.GetByIDScoped(ctx, tenantID, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrBankAccountNotFound
		}
		return nil, err
	}
	return domain.ToBankAccountResponse(account), nil
}

// mapBankAccountWriteError translates a storage rejection of the primary slot
// into the retryable 409 sentinel. bank_accounts carries no other unique
// constraint besides its primary key, so a duplicate-key rejection on a write
// that touches is_primary is always the concurrent-promoter case.
func mapBankAccountWriteError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrBankAccountNotFound) || errors.Is(err, domain.ErrBankAccountInvalid) || errors.Is(err, domain.ErrBankAccountInUse) || errors.Is(err, domain.ErrBankAccountConflict) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ErrBankAccountNotFound
	}
	if strings.Contains(err.Error(), "duplicate key value violates unique constraint") || strings.Contains(err.Error(), "uq_bank_accounts_tenant_primary") {
		return domain.ErrBankAccountConflict
	}
	return err
}
