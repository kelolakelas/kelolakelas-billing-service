package usecase

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

type PlatformWithdrawalUsecase interface {
	ListRequested(context.Context, domain.WithdrawalQuery) (*domain.PlatformWithdrawalListResponse, error)
	ListDecided(context.Context, domain.WithdrawalQuery) (*domain.PlatformWithdrawalListResponse, error)
	Decide(context.Context, uuid.UUID, uuid.UUID, string, string) (*domain.PlatformWithdrawalResponse, error)
}

type platformWithdrawalUsecase struct {
	withdrawals repository.PlatformWithdrawalRepository
	wallets     repository.WalletRepository
	ledger      repository.LedgerEntryRepository
	tx          repository.BillingTransactionManager
}

func NewPlatformWithdrawalUsecase(w repository.PlatformWithdrawalRepository, wallets repository.WalletRepository, ledger repository.LedgerEntryRepository, tx repository.BillingTransactionManager) PlatformWithdrawalUsecase {
	return &platformWithdrawalUsecase{withdrawals: w, wallets: wallets, ledger: ledger, tx: tx}
}

func (u *platformWithdrawalUsecase) ListRequested(ctx context.Context, query domain.WithdrawalQuery) (*domain.PlatformWithdrawalListResponse, error) {
	if err := validatePlatformWithdrawalQuery(query); err != nil {
		return nil, err
	}
	rows, total, err := u.withdrawals.ListRequested(ctx, query.Page, query.PageSize)
	if err != nil {
		return nil, err
	}
	return platformWithdrawalList(rows, total, query), nil
}

func (u *platformWithdrawalUsecase) ListDecided(ctx context.Context, query domain.WithdrawalQuery) (*domain.PlatformWithdrawalListResponse, error) {
	if err := validatePlatformWithdrawalQuery(query); err != nil {
		return nil, err
	}
	rows, total, err := u.withdrawals.ListDecided(ctx, query.Page, query.PageSize)
	if err != nil {
		return nil, err
	}
	return platformWithdrawalList(rows, total, query), nil
}

func validatePlatformWithdrawalQuery(query domain.WithdrawalQuery) error {
	if query.Page < 1 || query.PageSize < 1 || query.PageSize > 100 {
		return domain.ErrWithdrawalInvalid
	}
	return nil
}

func platformWithdrawalList(rows []domain.Withdrawal, total int64, query domain.WithdrawalQuery) *domain.PlatformWithdrawalListResponse {
	result := &domain.PlatformWithdrawalListResponse{Items: make([]domain.PlatformWithdrawalResponse, 0, len(rows))}
	for i := range rows {
		result.Items = append(result.Items, *domain.ToPlatformWithdrawalResponse(&rows[i]))
	}
	result.Pagination.Page, result.Pagination.PageSize, result.Pagination.TotalItems = query.Page, query.PageSize, total
	result.Pagination.TotalPages = int(math.Ceil(float64(total) / float64(query.PageSize)))
	return result
}

// Decide serializes on the wallet before claiming the withdrawal, matching the
// tenant cancel lock order. The claim, balance move and evidence share one transaction.
func (u *platformWithdrawalUsecase) Decide(ctx context.Context, adminID, id uuid.UUID, decision, detail string) (*domain.PlatformWithdrawalResponse, error) {
	detail = strings.TrimSpace(detail)
	if adminID == uuid.Nil || id == uuid.Nil || detail == "" || len(detail) > 255 && decision == domain.WithdrawalStatusPaid || len(detail) > 2000 && decision == domain.WithdrawalStatusRejected || (decision != domain.WithdrawalStatusPaid && decision != domain.WithdrawalStatusRejected) {
		return nil, domain.ErrWithdrawalInvalid
	}
	// Read only the immutable tenant id before locking; the status is rechecked
	// by the conditional UPDATE inside the transaction.
	target, err := u.withdrawals.GetByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrWithdrawalNotFound
	}
	if err != nil {
		return nil, err
	}
	var out *domain.Withdrawal
	err = u.tx.WithTransaction(ctx, func(ctx context.Context) error {
		locking, ok := u.wallets.(repository.WalletLockingRepository)
		if !ok {
			return fmt.Errorf("wallet locking unavailable")
		}
		wallet, err := locking.GetByTenantIDForUpdate(ctx, target.TenantID)
		if err != nil {
			return err
		}
		claimed, err := u.withdrawals.ClaimDecision(ctx, id, adminID, decision, detail, time.Now().UTC())
		if err != nil {
			return err
		}
		if !claimed {
			return domain.ErrWithdrawalInvalidState
		}
		current, err := u.withdrawals.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if current.TenantID != target.TenantID || current.Amount <= 0 || wallet.PendingBalance < current.Amount {
			return fmt.Errorf("invalid withdrawal hold state for %s", id)
		}
		wallet.PendingBalance -= current.Amount
		if decision == domain.WithdrawalStatusRejected {
			if wallet.AvailableBalance > math.MaxInt64-current.Amount {
				return fmt.Errorf("withdrawal balance overflow for %s", id)
			}
			wallet.AvailableBalance += current.Amount
		}
		if err := u.wallets.Update(ctx, wallet); err != nil {
			return err
		}
		description := fmt.Sprintf("Manual withdrawal %s for request %s", decision, id)
		if err := u.ledger.Create(ctx, &domain.LedgerEntry{ID: uuid.New(), WalletID: wallet.ID, ReferenceID: id, ReferenceType: domain.WithdrawalReferenceType, Amount: current.Amount, EntryType: domain.LedgerEntryTypeRelease, Description: &description}); err != nil {
			return err
		}
		if decision == domain.WithdrawalStatusPaid {
			if err := u.ledger.Create(ctx, &domain.LedgerEntry{ID: uuid.New(), WalletID: wallet.ID, ReferenceID: id, ReferenceType: domain.WithdrawalReferenceType, Amount: -current.Amount, EntryType: domain.LedgerEntryTypePaid, Description: &description}); err != nil {
				return err
			}
		}
		out = current
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_withdrawals_transfer_reference" {
			return nil, domain.ErrWithdrawalDuplicateReference
		}
		return nil, err
	}
	return domain.ToPlatformWithdrawalResponse(out), nil
}
