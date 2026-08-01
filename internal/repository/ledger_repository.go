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
	return r.db.WithContext(ctx).Create(entry).Error
}

func (r *ledgerEntryRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.LedgerEntry, error) {
	var entry domain.LedgerEntry
	if err := r.db.WithContext(ctx).First(&entry, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}
