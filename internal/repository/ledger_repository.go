package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type ledgerEntryRepository struct {
	db *gorm.DB
}

func NewLedgerEntryRepository(db *gorm.DB) LedgerEntryRepository {
	return &ledgerEntryRepository{db: db}
}

func (r *ledgerEntryRepository) Create(ctx context.Context, entry *domain.LedgerEntry) error {
	return GetDB(ctx, r.db).Create(entry).Error
}

func (r *ledgerEntryRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.LedgerEntry, error) {
	var entry domain.LedgerEntry
	if err := r.db.WithContext(ctx).First(&entry, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

// ListByWallet returns one wallet's entries newest first. Page starts at 1; a
// non-positive page or page size falls back to the API defaults so a caller
// cannot request an unbounded scan.
func (r *ledgerEntryRepository) ListByWallet(ctx context.Context, walletID uuid.UUID, page, pageSize int) ([]domain.LedgerEntry, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	if err := r.db.WithContext(ctx).Model(&domain.LedgerEntry{}).Where("wallet_id = ?", walletID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entries []domain.LedgerEntry
	if err := r.db.WithContext(ctx).Where("wallet_id = ?", walletID).Order("created_at DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&entries).Error; err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// SumByWallet totals one wallet's entries. The paid callback writes one
// `payment_received` entry per credited NetAmount, so on payment-only data this
// sum equals the wallet's AvailableBalance.
func (r *ledgerEntryRepository) SumByWallet(ctx context.Context, walletID uuid.UUID) (int64, error) {
	var total *int64
	if err := r.db.WithContext(ctx).Model(&domain.LedgerEntry{}).Where("wallet_id = ?", walletID).Select("COALESCE(SUM(amount), 0)").Scan(&total).Error; err != nil {
		return 0, err
	}
	if total == nil {
		return 0, nil
	}
	return *total, nil
}
