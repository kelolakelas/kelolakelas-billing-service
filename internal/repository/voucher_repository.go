package repository

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type voucherRepository struct {
	db *gorm.DB
}

func NewVoucherRepository(db *gorm.DB) VoucherRepository {
	return &voucherRepository{db: db}
}

// LockTenantCodes takes a transaction-scoped PostgreSQL advisory lock before
// checking a code and writing it. Serializing per tenant closes the gap where
// two concurrent writes both miss a legacy mixed-case row (or each other).
func (r *voucherRepository) LockTenantCodes(ctx context.Context, tenantID uuid.UUID) error {
	return GetDB(ctx, r.db).Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", tenantID.String()).Error
}

func (r *voucherRepository) Create(ctx context.Context, voucher *domain.Voucher) error {
	return GetDB(ctx, r.db).Create(voucher).Error
}

// GetByIDScoped answers gorm.ErrRecordNotFound for a missing row, a
// soft-deleted row, or a row of another tenant, so callers cannot probe which
// tenants own which vouchers. GORM's soft-delete scope already excludes
// deleted_at rows.
func (r *voucherRepository) GetByIDScoped(ctx context.Context, tenantID, id uuid.UUID) (*domain.Voucher, error) {
	var voucher domain.Voucher
	if err := GetDB(ctx, r.db).First(&voucher, "tenant_id = ? AND id = ?", tenantID, id).Error; err != nil {
		return nil, err
	}
	return &voucher, nil
}

// GetByCode matches case-insensitively within the tenant: codes are stored in
// their canonical (uppercased) form, but LOWER() keeps lookups correct for
// rows written before the canonicalization or through another path.
func (r *voucherRepository) GetByCode(ctx context.Context, tenantID uuid.UUID, code string) (*domain.Voucher, error) {
	var voucher domain.Voucher
	if err := GetDB(ctx, r.db).First(&voucher, "tenant_id = ? AND LOWER(code) = LOWER(?)", tenantID, strings.TrimSpace(code)).Error; err != nil {
		return nil, err
	}
	return &voucher, nil
}

func (r *voucherRepository) HasCodeOtherThan(ctx context.Context, tenantID, excludeID uuid.UUID, code string) (bool, error) {
	var count int64
	err := GetDB(ctx, r.db).Model(&domain.Voucher{}).
		Where("tenant_id = ? AND id <> ? AND LOWER(code) = LOWER(?)", tenantID, excludeID, strings.TrimSpace(code)).
		Count(&count).Error
	return count > 0, err
}

// GetByIDScopedForUpdate is the locked variant for write transactions: the
// update path re-checks max_uses against the locked current_uses, so a
// concurrent checkout redemption (KEL-29) cannot slip under a lowered cap.
func (r *voucherRepository) GetByIDScopedForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Voucher, error) {
	var voucher domain.Voucher
	if err := GetDB(ctx, r.db).Clauses(clause.Locking{Strength: "UPDATE"}).First(&voucher, "tenant_id = ? AND id = ?", tenantID, id).Error; err != nil {
		return nil, err
	}
	return &voucher, nil
}

// ListByTenant returns one tenant's live vouchers newest first. Page starts
// at 1; a non-positive page or page size falls back to the API defaults so a
// caller cannot request an unbounded scan.
func (r *voucherRepository) ListByTenant(ctx context.Context, tenantID uuid.UUID, page, pageSize int) ([]domain.Voucher, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	if err := GetDB(ctx, r.db).Model(&domain.Voucher{}).Where("tenant_id = ?", tenantID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []domain.Voucher
	if err := GetDB(ctx, r.db).Where("tenant_id = ?", tenantID).Order("created_at DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *voucherRepository) Update(ctx context.Context, voucher *domain.Voucher) error {
	return GetDB(ctx, r.db).Save(voucher).Error
}

// SoftDelete marks the row deleted; history (transactions referencing it)
// keeps its foreign key. Only rows never consumed reach here: the usecase
// refuses used vouchers before this is called.
func (r *voucherRepository) SoftDelete(ctx context.Context, tenantID, id uuid.UUID) (bool, error) {
	result := GetDB(ctx, r.db).Where("tenant_id = ? AND id = ?", tenantID, id).Delete(&domain.Voucher{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}
