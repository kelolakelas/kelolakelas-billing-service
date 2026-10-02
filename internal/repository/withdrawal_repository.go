package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type withdrawalRepository struct {
	db *gorm.DB
}

func NewWithdrawalRepository(db *gorm.DB) *withdrawalRepository {
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

// GetByIDScoped answers gorm.ErrRecordNotFound for a missing row or a row of
// another tenant, so callers cannot probe which tenants own which withdrawals.
func (r *withdrawalRepository) GetByIDScoped(ctx context.Context, tenantID, id uuid.UUID) (*domain.Withdrawal, error) {
	var withdrawal domain.Withdrawal
	if err := GetDB(ctx, r.db).First(&withdrawal, "tenant_id = ? AND id = ?", tenantID, id).Error; err != nil {
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

// CountOpenByTenant counts the tenant's withdrawals that still hold balance.
// It runs under the caller's wallet row lock, so two concurrent requests
// serialize on the lock and exactly one passes the single-open guard.
func (r *withdrawalRepository) CountOpenByTenant(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	var count int64
	if err := GetDB(ctx, r.db).Model(&domain.Withdrawal{}).Where("tenant_id = ? AND status IN ?", tenantID, domain.WithdrawalActiveStatuses).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// GetByTenantAndKey returns the tenant's request stored under the idempotency
// key, or gorm.ErrRecordNotFound when the key was never used.
func (r *withdrawalRepository) GetByTenantAndKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Withdrawal, error) {
	var withdrawal domain.Withdrawal
	if err := GetDB(ctx, r.db).First(&withdrawal, "tenant_id = ? AND idempotency_key = ?", tenantID, key).Error; err != nil {
		return nil, err
	}
	return &withdrawal, nil
}

// ListByTenant returns one tenant's withdrawals newest first. Page starts at
// 1; a non-positive page or page size falls back to the API defaults so a
// caller cannot request an unbounded scan.
func (r *withdrawalRepository) ListByTenant(ctx context.Context, tenantID uuid.UUID, page, pageSize int) ([]domain.Withdrawal, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	if err := GetDB(ctx, r.db).Model(&domain.Withdrawal{}).Where("tenant_id = ?", tenantID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []domain.Withdrawal
	if err := GetDB(ctx, r.db).Where("tenant_id = ?", tenantID).Order("requested_at DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ClaimCancelled moves a `requested` withdrawal of the tenant to `cancelled`
// and stamps cancelled_at in one conditional statement. It reports whether a
// row was actually moved: of any number of concurrent cancellers (or a tenant
// cancel racing a future admin processing step) exactly one reports true and
// the losers see the state the winner left instead of overwriting it.
// ListRequested is the oldest-first cross-tenant operator queue.
func (r *withdrawalRepository) ListRequested(ctx context.Context, page, pageSize int) ([]domain.Withdrawal, int64, error) {
	var total int64
	db := GetDB(ctx, r.db).Model(&domain.Withdrawal{}).Where("status = ?", domain.WithdrawalStatusRequested)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []domain.Withdrawal
	err := db.Order("requested_at ASC").Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

// ListDecided includes only manual terminal decisions, newest first. The UUID
// tie-break keeps offset pagination deterministic when timestamps are equal.
func (r *withdrawalRepository) ListDecided(ctx context.Context, page, pageSize int) ([]domain.Withdrawal, int64, error) {
	statuses := []string{domain.WithdrawalStatusPaid, domain.WithdrawalStatusRejected}
	db := GetDB(ctx, r.db).Model(&domain.Withdrawal{}).Where("status IN ?", statuses)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []domain.Withdrawal
	err := db.Order("decided_at DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

func (r *withdrawalRepository) ClaimDecision(ctx context.Context, id, adminID uuid.UUID, decision, detail string, now time.Time) (bool, error) {
	updates := map[string]interface{}{"status": decision, "decided_by": adminID, "decided_at": now, "processed_at": now}
	if decision == domain.WithdrawalStatusPaid {
		updates["transfer_reference"] = detail
	} else {
		updates["reject_reason"] = detail
	}
	result := GetDB(ctx, r.db).Model(&domain.Withdrawal{}).Where("id = ? AND status = ?", id, domain.WithdrawalStatusRequested).Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (r *withdrawalRepository) ClaimCancelled(ctx context.Context, tenantID, id uuid.UUID, now time.Time) (bool, error) {
	result := GetDB(ctx, r.db).Model(&domain.Withdrawal{}).
		Where("id = ? AND tenant_id = ? AND status = ?", id, tenantID, domain.WithdrawalStatusRequested).
		Updates(map[string]interface{}{"status": domain.WithdrawalStatusCancelled, "cancelled_at": now})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}
