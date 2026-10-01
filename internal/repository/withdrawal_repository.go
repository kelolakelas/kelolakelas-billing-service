package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type withdrawalRepository struct {
	db *gorm.DB
}

func NewWithdrawalRepository(db *gorm.DB) WithdrawalRepository {
	return &withdrawalRepository{db: db}
}

func (r *withdrawalRepository) Create(ctx context.Context, withdrawal *domain.Withdrawal) error {
	return GetDB(ctx, r.db).Create(withdrawal).Error
}

func (r *withdrawalRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Withdrawal, error) {
	var withdrawal domain.Withdrawal
	if err := GetDB(ctx, r.db).First(&withdrawal, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &withdrawal, nil
}

func (r *withdrawalRepository) Update(ctx context.Context, withdrawal *domain.Withdrawal) error {
	return GetDB(ctx, r.db).Save(withdrawal).Error
}

func (r *withdrawalRepository) CountActiveByBankAccount(ctx context.Context, accountID uuid.UUID, statuses []string) (int64, error) {
	var count int64
	if len(statuses) == 0 {
		return 0, nil
	}
	if err := GetDB(ctx, r.db).Model(&domain.Withdrawal{}).Where("bank_account_id = ? AND status IN ?", accountID, statuses).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
