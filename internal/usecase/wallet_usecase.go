package usecase

import (
	"context"
	"errors"
	"math"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

type walletUsecase struct {
	wallets repository.WalletRepository
	ledger  repository.LedgerEntryRepository
}

// NewWalletUsecase serves the tenant wallet balance and ledger reads (KEL-142).
func NewWalletUsecase(wallets repository.WalletRepository, ledger repository.LedgerEntryRepository) WalletUsecase {
	return &walletUsecase{wallets: wallets, ledger: ledger}
}

// GetBalance mirrors the stored wallet row. A tenant without a wallet gets a
// zero balance, not an error: sandbox payments never credit the wallet, and a
// tenant that never received a paid callback simply has nothing yet.
func (u *walletUsecase) GetBalance(ctx context.Context, tenantID uuid.UUID) (*domain.WalletBalanceResponse, error) {
	wallet, err := u.wallets.GetByTenantID(ctx, tenantID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &domain.WalletBalanceResponse{}, nil
		}
		return nil, err
	}
	return &domain.WalletBalanceResponse{
		AvailableBalance: wallet.AvailableBalance,
		PendingBalance:   wallet.PendingBalance,
	}, nil
}

// ListLedger returns one tenant's ledger mutations newest first. The wallet is
// the tenant boundary: entries are addressed through the wallet id resolved
// from the verified tenant claim, so another tenant's entries are unreachable.
func (u *walletUsecase) ListLedger(ctx context.Context, tenantID uuid.UUID, query domain.LedgerQuery) (*domain.LedgerListResponse, error) {
	page, pageSize := query.Page, query.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	wallet, err := u.wallets.GetByTenantID(ctx, tenantID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return emptyLedgerPage(page, pageSize), nil
		}
		return nil, err
	}
	entries, total, err := u.ledger.ListByWallet(ctx, wallet.ID, page, pageSize)
	if err != nil {
		return nil, err
	}
	items := make([]domain.LedgerEntryResponse, 0, len(entries))
	for i := range entries {
		e := entries[i]
		items = append(items, domain.LedgerEntryResponse{
			ID:            e.ID,
			ReferenceID:   e.ReferenceID,
			ReferenceType: e.ReferenceType,
			Amount:        e.Amount,
			EntryType:     e.EntryType,
			Description:   e.Description,
			CreatedAt:     e.CreatedAt,
		})
	}
	out := &domain.LedgerListResponse{Items: items}
	out.Pagination.Page = page
	out.Pagination.PageSize = pageSize
	out.Pagination.TotalItems = total
	out.Pagination.TotalPages = int(math.Ceil(float64(total) / float64(pageSize)))
	return out, nil
}

func emptyLedgerPage(page, pageSize int) *domain.LedgerListResponse {
	out := &domain.LedgerListResponse{Items: []domain.LedgerEntryResponse{}}
	out.Pagination.Page = page
	out.Pagination.PageSize = pageSize
	return out
}
