package usecase

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"gorm.io/gorm"
	"strings"
	"time"
)

func WithVoucherReservations(service TransactionUsecase, vouchers repository.VoucherReservationRepository) TransactionUsecase {
	if u, ok := service.(*transactionUsecase); ok {
		u.vouchers = vouchers
	}
	return service
}

// Insert the authoritative checkout snapshot and reservation together before
// contacting any provider. Enrollment locking makes retries quota-idempotent.
func (u *transactionUsecase) prepareVoucherCheckout(ctx context.Context, req *domain.GenerateSubscriptionPaymentRequest) error {
	if req.PaymentMethod != "" && req.PaymentMethod != "VC" && req.PaymentMethod != "VA" && req.PaymentMethod != "BC" && req.PaymentMethod != "SP" && req.PaymentMethod != "NQ" {
		return domain.ErrInvalidPaymentMethod
	}
	if _, err := nextBillingDate(time.Now().UTC(), req.BillingCycle); err != nil {
		return err
	}
	if u.txManager == nil {
		return domain.ErrVoucherRejected
	}
	locker, ok := u.vouchers.(interface {
		LockCheckout(context.Context, uuid.UUID) error
	})
	if !ok {
		return domain.ErrVoucherRejected
	}
	return u.txManager.WithTransaction(ctx, func(tc context.Context) error {
		if err := locker.LockCheckout(tc, req.EnrollmentID); err != nil {
			return err
		}
		old, err := u.txRepo.GetByEnrollmentID(tc, req.EnrollmentID)
		if err == nil {
			req.SubtotalAmount = old.SubtotalAmount
			req.DiscountAmount = old.DiscountAmount
			req.VoucherID = old.VoucherID
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if req.SubtotalAmount <= 0 {
			return domain.ErrVoucherRejected
		}
		now := time.Now().UTC()
		if strings.TrimSpace(req.VoucherCode) != "" {
			v, reserved, err := u.vouchers.ReserveUseForCode(tc, req.TenantID, req.VoucherCode, now)
			if err != nil {
				return err
			}
			if !reserved {
				return domain.ErrVoucherRejected
			}
			if err := domain.ValidateVoucherForRedemption(v, domain.VoucherRedeemValidation{Now: now, Code: req.VoucherCode, Amount: req.SubtotalAmount}); err != nil {
				return err
			}
			req.VoucherID = &v.ID
			req.DiscountAmount = domain.ComputeVoucherDiscount(v, req.SubtotalAmount)
		}
		gross := req.SubtotalAmount - req.DiscountAmount
		fees, err := newTransactionFees(tc, u.feePolicy, gross, req.PaymentGatewayFee)
		if err != nil {
			return err
		}
		method := req.PaymentMethod
		if method == "" {
			method = "VC"
		}
		provider := "duitku"
		tx := &domain.Transaction{ID: req.EnrollmentID, MerchantOrderID: req.EnrollmentID.String(), TenantID: req.TenantID, ParentID: req.ParentID, StudentID: req.StudentID, EnrollmentID: req.EnrollmentID, VoucherID: req.VoucherID, SubtotalAmount: req.SubtotalAmount, DiscountAmount: req.DiscountAmount, GrossAmount: gross, PaymentGatewayFee: req.PaymentGatewayFee, Currency: "IDR", Status: domain.TransactionStatusPending, PaymentMethod: &method, PaymentGatewayProvider: &provider, BillingEmail: req.SenderEmail, ClassName: req.Title, IsSandbox: strings.Contains(strings.ToLower(u.cfg.DuitkuAPIBaseURL), "sandbox")}
		fees.Apply(tx)
		return u.txRepo.Create(tc, tx)
	})
}

type VoucherPreviewRequest struct {
	TenantID       uuid.UUID `json:"tenant_id" binding:"required"`
	SubtotalAmount int64     `json:"subtotal_amount" binding:"required,gt=0"`
	VoucherCode    string    `json:"voucher_code" binding:"required"`
}
type VoucherPreviewResponse struct {
	DiscountAmount int64 `json:"discount_amount"`
	GrossAmount    int64 `json:"gross_amount"`
}
type VoucherPreviewUsecase struct {
	vouchers repository.VoucherReservationRepository
}

func NewVoucherPreviewUsecase(v repository.VoucherReservationRepository) *VoucherPreviewUsecase {
	return &VoucherPreviewUsecase{vouchers: v}
}
func (u *VoucherPreviewUsecase) Preview(ctx context.Context, req VoucherPreviewRequest) (*VoucherPreviewResponse, error) {
	if req.TenantID == uuid.Nil {
		return nil, domain.ErrVoucherRejected
	}
	now := time.Now().UTC()
	v, err := u.vouchers.GetForPreviewByCode(ctx, req.TenantID, req.VoucherCode, now)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrVoucherRejected
	}
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateVoucherForRedemption(v, domain.VoucherRedeemValidation{Now: now, Code: req.VoucherCode, Amount: req.SubtotalAmount}); err != nil {
		return nil, err
	}
	discount := domain.ComputeVoucherDiscount(v, req.SubtotalAmount)
	return &VoucherPreviewResponse{DiscountAmount: discount, GrossAmount: req.SubtotalAmount - discount}, nil
}
