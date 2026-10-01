package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type bankAccountRepository struct {
	db *gorm.DB
}

func NewBankAccountRepository(db *gorm.DB) BankAccountRepository {
	return &bankAccountRepository{db: db}
}

func (r *bankAccountRepository) Create(ctx context.Context, account *domain.BankAccount) error {
	return GetDB(ctx, r.db).Create(account).Error
}

func (r *bankAccountRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.BankAccount, error) {
	var account domain.BankAccount
	if err := GetDB(ctx, r.db).First(&account, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

// GetByIDScoped answers gorm.ErrRecordNotFound for a missing row, a soft-deleted
// row, or a row of another tenant, so callers cannot probe which tenants own
// which accounts. GORM's soft-delete scope already excludes deleted_at rows.
func (r *bankAccountRepository) GetByIDScoped(ctx context.Context, tenantID, id uuid.UUID) (*domain.BankAccount, error) {
	var account domain.BankAccount
	if err := GetDB(ctx, r.db).First(&account, "tenant_id = ? AND id = ?", tenantID, id).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (r *bankAccountRepository) GetByIDScopedForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.BankAccount, error) {
	var account domain.BankAccount
	if err := GetDB(ctx, r.db).Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, "tenant_id = ? AND id = ?", tenantID, id).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (r *bankAccountRepository) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.BankAccount, error) {
	var accounts []domain.BankAccount
	if err := GetDB(ctx, r.db).Where("tenant_id = ?", tenantID).Order("is_primary DESC").Order("created_at ASC").Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

func (r *bankAccountRepository) HasPrimary(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	var count int64
	if err := GetDB(ctx, r.db).Model(&domain.BankAccount{}).Where("tenant_id = ? AND is_primary = ?", tenantID, true).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *bankAccountRepository) ClearPrimary(ctx context.Context, tenantID uuid.UUID) error {
	return GetDB(ctx, r.db).Model(&domain.BankAccount{}).Where("tenant_id = ? AND is_primary = ?", tenantID, true).Update("is_primary", false).Error
}

// SetPrimary clears the tenant's primary slot and promotes the target in the
// caller's transaction. The partial unique index uq_bank_accounts_tenant_primary
// is the backstop: concurrent promoters serialize on it and exactly one wins.
func (r *bankAccountRepository) SetPrimary(ctx context.Context, tenantID, id uuid.UUID) error {
	db := GetDB(ctx, r.db)
	if err := db.Model(&domain.BankAccount{}).Where("tenant_id = ? AND is_primary = ?", tenantID, true).Update("is_primary", false).Error; err != nil {
		return err
	}
	result := db.Model(&domain.BankAccount{}).Where("tenant_id = ? AND id = ?", tenantID, id).Update("is_primary", true)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *bankAccountRepository) Update(ctx context.Context, account *domain.BankAccount) error {
	return GetDB(ctx, r.db).Save(account).Error
}

// SoftDelete marks the row deleted and reports whether a live row of this
// tenant was actually affected. History rows are never removed.
func (r *bankAccountRepository) SoftDelete(ctx context.Context, tenantID, id uuid.UUID) (bool, error) {
	result := GetDB(ctx, r.db).Where("tenant_id = ? AND id = ?", tenantID, id).Delete(&domain.BankAccount{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}
