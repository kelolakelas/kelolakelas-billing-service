package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type walletRepository struct {
	db *gorm.DB
}

func NewWalletRepository(db *gorm.DB) WalletRepository {
	return &walletRepository{db: db}
}

func (r *walletRepository) GetByTenantID(ctx context.Context, tenantID uuid.UUID) (*domain.Wallet, error) {
	var wallet domain.Wallet
	if err := r.db.WithContext(ctx).First(&wallet, "tenant_id = ?", tenantID).Error; err != nil {
		return nil, err
	}
	return &wallet, nil
}

func (r *walletRepository) Create(ctx context.Context, wallet *domain.Wallet) error {
	return r.db.WithContext(ctx).Create(wallet).Error
}

func (r *walletRepository) Update(ctx context.Context, wallet *domain.Wallet) error {
	return r.db.WithContext(ctx).Save(wallet).Error
}
