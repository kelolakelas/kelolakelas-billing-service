package repository

import (
	"context"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type voucherReservation struct{ db *gorm.DB }

func NewVoucherReservation(db *gorm.DB) VoucherReservationRepository {
	return &voucherReservation{db: db}
}
func (r *voucherReservation) getDB(ctx context.Context) *gorm.DB { return GetDB(ctx, r.db) }

// ReserveUseForCode must be called inside the checkout transaction. The locked
// snapshot and increment roll back together if validation or insertion fails.
func (r *voucherReservation) ReserveUseForCode(ctx context.Context, tenantID uuid.UUID, code string, now time.Time) (*domain.Voucher, bool, error) {
	var v domain.Voucher
	err := r.getDB(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND LOWER(code) = LOWER(?)", tenantID, domain.NormalizeVoucherCode(code)).First(&v).Error
	if err == gorm.ErrRecordNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !v.IsActive || (v.ValidFrom != nil && now.Before(*v.ValidFrom)) || (v.ValidUntil != nil && now.After(*v.ValidUntil)) || domain.VoucherRedeemQuotaLeft(&v) <= 0 {
		return nil, false, nil
	}
	err = r.getDB(ctx).Model(&v).Update("current_uses", gorm.Expr("current_uses + 1")).Error
	return &v, err == nil, err
}
func (r *voucherReservation) GetForPreviewByCode(ctx context.Context, tenantID uuid.UUID, code string, now time.Time) (*domain.Voucher, error) {
	var v domain.Voucher
	err := r.getDB(ctx).Where("tenant_id = ? AND LOWER(code) = LOWER(?)", tenantID, domain.NormalizeVoucherCode(code)).First(&v).Error
	if err != nil {
		return nil, err
	}
	if !v.IsActive || (v.ValidFrom != nil && now.Before(*v.ValidFrom)) || (v.ValidUntil != nil && now.After(*v.ValidUntil)) || domain.VoucherRedeemQuotaLeft(&v) <= 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &v, nil
}

// Serialize enrollment insertion before reserving quota; retries never take a second use.
func (r *voucherReservation) LockCheckout(ctx context.Context, id uuid.UUID) error {
	return r.getDB(ctx).Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 162))", id.String()).Error
}
