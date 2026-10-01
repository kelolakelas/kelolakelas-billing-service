package usecase

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

type voucherUsecase struct {
	vouchers  repository.VoucherRepository
	txManager repository.BillingTransactionManager
}

// NewVoucherUsecase serves the tenant voucher management (KEL-161). A nil
// txManager runs writes without a transaction; production always wires the
// real manager.
func NewVoucherUsecase(vouchers repository.VoucherRepository, txManager repository.BillingTransactionManager) VoucherUsecase {
	return &voucherUsecase{vouchers: vouchers, txManager: txManager}
}

func (u *voucherUsecase) List(ctx context.Context, tenantID uuid.UUID, query domain.VoucherQuery) (*domain.VoucherListResponse, error) {
	page, pageSize := query.Page, query.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	rows, total, err := u.vouchers.ListByTenant(ctx, tenantID, page, pageSize)
	if err != nil {
		return nil, err
	}
	items := make([]domain.VoucherResponse, 0, len(rows))
	for i := range rows {
		items = append(items, *domain.ToVoucherResponse(&rows[i]))
	}
	out := &domain.VoucherListResponse{Items: items}
	out.Pagination.Page = page
	out.Pagination.PageSize = pageSize
	out.Pagination.TotalItems = total
	out.Pagination.TotalPages = int(math.Ceil(float64(total) / float64(pageSize)))
	return out, nil
}

func (u *voucherUsecase) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.VoucherResponse, error) {
	if id == uuid.Nil {
		return nil, domain.ErrVoucherInvalid
	}
	voucher, err := u.vouchers.GetByIDScoped(ctx, tenantID, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrVoucherNotFound
		}
		return nil, err
	}
	return domain.ToVoucherResponse(voucher), nil
}

func (u *voucherUsecase) Create(ctx context.Context, tenantID uuid.UUID, req *domain.CreateVoucherRequest) (*domain.VoucherResponse, error) {
	if req == nil {
		return nil, domain.ErrVoucherInvalid
	}
	code := domain.NormalizeVoucherCode(req.Code)
	if err := domain.ValidateVoucherFields(code, req.DiscountType, req.DiscountValue, req.MinTransactionAmount, req.MaxDiscountAmount, req.MaxUses, 0, req.ValidFrom, req.ValidUntil); err != nil {
		return nil, err
	}
	if req.MinTransactionAmount < 0 {
		return nil, domain.ErrVoucherInvalid
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	voucher := &domain.Voucher{
		ID:                   uuid.New(),
		TenantID:             tenantID,
		Code:                 code,
		DiscountType:         req.DiscountType,
		DiscountValue:        req.DiscountValue,
		MaxDiscountAmount:    req.MaxDiscountAmount,
		MinTransactionAmount: req.MinTransactionAmount,
		MaxUses:              req.MaxUses,
		IsActive:             isActive,
		ValidFrom:            req.ValidFrom,
		ValidUntil:           req.ValidUntil,
	}
	create := func(ctx context.Context) error {
		if err := u.vouchers.LockTenantCodes(ctx, tenantID); err != nil {
			return err
		}
		// Existing rows may predate canonical storage. Check under the tenant
		// lock so concurrent code writes cannot both miss a legacy variant.
		if err := u.requireCodeFree(ctx, tenantID, uuid.Nil, code); err != nil {
			return err
		}
		return u.vouchers.Create(ctx, voucher)
	}
	if u.txManager != nil {
		if err := u.txManager.WithTransaction(ctx, create); err != nil {
			return nil, mapVoucherWriteError(err)
		}
	} else if err := create(ctx); err != nil {
		return nil, mapVoucherWriteError(err)
	}
	return domain.ToVoucherResponse(voucher), nil
}

func (u *voucherUsecase) Update(ctx context.Context, tenantID, id uuid.UUID, req *domain.UpdateVoucherRequest) (*domain.VoucherResponse, error) {
	if id == uuid.Nil || req == nil {
		return nil, domain.ErrVoucherInvalid
	}
	if req.Code == nil && req.DiscountType == nil && req.DiscountValue == nil && req.MaxDiscountAmount == nil &&
		req.MinTransactionAmount == nil && req.MaxUses == nil && req.ValidFrom == nil && req.ValidUntil == nil && req.IsActive == nil {
		return nil, domain.ErrVoucherInvalid
	}
	var voucher *domain.Voucher
	update := func(ctx context.Context) error {
		// Acquire the tenant code lock before the row lock, in the same order
		// as other code writes, to avoid deadlocks during concurrent renames.
		if req.Code != nil {
			if err := u.vouchers.LockTenantCodes(ctx, tenantID); err != nil {
				return err
			}
		}
		var err error
		voucher, err = u.lockedVoucher(ctx, tenantID, id)
		if err != nil {
			return err
		}
		code := voucher.Code
		if req.Code != nil {
			code = domain.NormalizeVoucherCode(*req.Code)
		}
		discountType := voucher.DiscountType
		if req.DiscountType != nil {
			discountType = *req.DiscountType
		}
		discountValue := voucher.DiscountValue
		if req.DiscountValue != nil {
			discountValue = *req.DiscountValue
		}
		maxDiscount := voucher.MaxDiscountAmount
		if req.MaxDiscountAmount != nil {
			maxDiscount = req.MaxDiscountAmount
		}
		minAmount := voucher.MinTransactionAmount
		if req.MinTransactionAmount != nil {
			minAmount = *req.MinTransactionAmount
		}
		maxUses := voucher.MaxUses
		if req.MaxUses != nil {
			maxUses = req.MaxUses
		}
		validFrom := voucher.ValidFrom
		if req.ValidFrom != nil {
			validFrom = req.ValidFrom
		}
		validUntil := voucher.ValidUntil
		if req.ValidUntil != nil {
			validUntil = req.ValidUntil
		}
		// The usage cap is validated against the locked current_uses, so a
		// concurrent checkout redemption (KEL-29) that consumed the last use
		// cannot slip under a lowered cap between the read and the write.
		if err := domain.ValidateVoucherFields(code, discountType, discountValue, minAmount, maxDiscount, maxUses, voucher.CurrentUses, validFrom, validUntil); err != nil {
			return err
		}
		if req.Code != nil {
			if err := u.requireCodeFree(ctx, tenantID, id, code); err != nil {
				return err
			}
			voucher.Code = code
		}
		voucher.DiscountType = discountType
		voucher.DiscountValue = discountValue
		voucher.MaxDiscountAmount = maxDiscount
		voucher.MinTransactionAmount = minAmount
		voucher.MaxUses = maxUses
		voucher.ValidFrom = validFrom
		voucher.ValidUntil = validUntil
		if req.IsActive != nil {
			voucher.IsActive = *req.IsActive
		}
		voucher.UpdatedAt = time.Now()
		return u.vouchers.Update(ctx, voucher)
	}
	if u.txManager != nil {
		if err := u.txManager.WithTransaction(ctx, update); err != nil {
			return nil, mapVoucherWriteError(err)
		}
	} else if err := update(ctx); err != nil {
		return nil, mapVoucherWriteError(err)
	}
	return domain.ToVoucherResponse(voucher), nil
}

// Delete removes a voucher that never discounted a transaction. A voucher
// with CurrentUses > 0 answers ErrVoucherInUse: history must survive, so it
// can only be deactivated through Update, never deleted.
func (u *voucherUsecase) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	if id == uuid.Nil {
		return domain.ErrVoucherInvalid
	}
	var inUse bool
	remove := func(ctx context.Context) error {
		voucher, err := u.lockedVoucher(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if voucher.CurrentUses > 0 {
			inUse = true
			return domain.ErrVoucherInUse
		}
		deleted, err := u.vouchers.SoftDelete(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if !deleted {
			return domain.ErrVoucherNotFound
		}
		return nil
	}
	if u.txManager != nil {
		if err := u.txManager.WithTransaction(ctx, remove); err != nil {
			if errors.Is(err, domain.ErrVoucherInUse) {
				return domain.ErrVoucherInUse
			}
			return mapVoucherWriteError(err)
		}
		return nil
	}
	if err := remove(ctx); err != nil {
		if errors.Is(err, domain.ErrVoucherInUse) {
			return domain.ErrVoucherInUse
		}
		return mapVoucherWriteError(err)
	}
	if inUse {
		return domain.ErrVoucherInUse
	}
	return nil
}

// lockedVoucher reads the row under a write lock when the repository offers
// it, so concurrent updates and future checkout reservations serialize on
// the row. A foreign or missing id is ErrVoucherNotFound either way.
func (u *voucherUsecase) lockedVoucher(ctx context.Context, tenantID, id uuid.UUID) (*domain.Voucher, error) {
	if locking, ok := u.vouchers.(interface {
		GetByIDScopedForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Voucher, error)
	}); ok {
		voucher, err := locking.GetByIDScopedForUpdate(ctx, tenantID, id)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, domain.ErrVoucherNotFound
			}
			return nil, err
		}
		return voucher, nil
	}
	voucher, err := u.vouchers.GetByIDScoped(ctx, tenantID, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrVoucherNotFound
		}
		return nil, err
	}
	return voucher, nil
}

// requireCodeFree checks for any other live voucher with a case-insensitive
// match. It runs under the tenant code lock for both create and rename.
func (u *voucherUsecase) requireCodeFree(ctx context.Context, tenantID, self uuid.UUID, code string) error {
	exists, err := u.vouchers.HasCodeOtherThan(ctx, tenantID, self, code)
	if err != nil {
		return err
	}
	if exists {
		return domain.ErrVoucherDuplicate
	}
	return nil
}

// mapVoucherWriteError translates storage rejections: the unique backstop
// firing on a concurrent create with the same code is a duplicate, whatever
// case the two writers used.
func mapVoucherWriteError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrVoucherNotFound) || errors.Is(err, domain.ErrVoucherInvalid) ||
		errors.Is(err, domain.ErrVoucherDuplicate) || errors.Is(err, domain.ErrVoucherInUse) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ErrVoucherNotFound
	}
	msg := err.Error()
	if containsConstraint(msg, "uq_vouchers_tenant_code") || containsConstraint(msg, "duplicate key value violates unique constraint") {
		return domain.ErrVoucherDuplicate
	}
	return err
}

func containsConstraint(msg, sub string) bool {
	if len(msg) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(msg); i++ {
		if msg[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
