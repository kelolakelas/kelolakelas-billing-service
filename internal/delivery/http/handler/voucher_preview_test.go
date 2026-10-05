package handler

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
	"gorm.io/gorm"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type previewRepo struct {
	repository.VoucherReservationRepository
	voucher *domain.Voucher
	err     error
}

func (r previewRepo) GetForPreviewByCode(context.Context, uuid.UUID, string, time.Time) (*domain.Voucher, error) {
	return r.voucher, r.err
}
func TestKEL162PreviewEnvelopeAndErrors(t *testing.T) {
	for _, tt := range []struct {
		name     string
		repo     previewRepo
		code     int
		fragment string
	}{
		{"success", previewRepo{voucher: &domain.Voucher{Code: "SAVE", IsActive: true, DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10}}, 200, `"discount_amount":1000,"gross_amount":9003`},
		{"rejected", previewRepo{err: gorm.ErrRecordNotFound}, 422, `"code":"voucher_rejected"`},
		{"sanitized", previewRepo{err: fmt.Errorf("secret database failure")}, 500, "Failed to preview voucher"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.POST("/preview", VoucherPreview(usecase.NewVoucherPreviewUsecase(tt.repo)))
			req := httptest.NewRequest("POST", "/preview", strings.NewReader(`{"tenant_id":"`+uuid.NewString()+`","subtotal_amount":10003,"voucher_code":"SAVE"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tt.code || !strings.Contains(w.Body.String(), tt.fragment) || strings.Contains(w.Body.String(), "secret database") {
				t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
func TestKEL162CheckoutVoucherRejectedEnvelope(t *testing.T) {
	w := callGenerateWithError(t, fmt.Errorf("wrapped: %w", domain.ErrVoucherRejected))
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"code":"voucher_rejected"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
