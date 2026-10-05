package handler

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"gorm.io/gorm"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type refundHandlerStub struct {
	calls             int
	tenant, actor, id uuid.UUID
	req               domain.RefundRequest
	err               error
}

func (s *refundHandlerStub) RecordRefund(_ context.Context, tenant, actor, id uuid.UUID, req domain.RefundRequest) (*domain.TransactionRefund, error) {
	s.calls++
	s.tenant, s.actor, s.id, s.req = tenant, actor, id, req
	return &domain.TransactionRefund{TransactionID: id}, s.err
}
func (*refundHandlerStub) ProcessRefund(context.Context, time.Time, func(context.Context, uuid.UUID) error) error {
	return nil
}
func TestRefundHandlerValidationAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		err        error
		want       int
	}{{"valid", `{"reason":"refund","transfer_reference":"bank"}`, nil, 200}, {"invalid body", `{}`, nil, 400}, {"foreign", `{"reason":"refund","transfer_reference":"bank"}`, gorm.ErrRecordNotFound, 404}, {"non paid", `{"reason":"refund","transfer_reference":"bank"}`, domain.ErrInvalidTransactionStatus, 409}, {"blank", `{"reason":" ","transfer_reference":"bank"}`, repository.ErrInvalidRefund, 400}, {"database unavailable", `{"reason":"refund","transfer_reference":"bank"}`, errors.New("offline"), 500}} {
		t.Run(tc.name, func(t *testing.T) {
			tenant, actor, id := uuid.New(), uuid.New(), uuid.New()
			stub := &refundHandlerStub{err: tc.err}
			h := NewRefundHandler(stub)
			r := gin.New()
			r.Use(func(c *gin.Context) { c.Set("tenant_id", tenant.String()); c.Set("user_id", actor.String()) })
			r.POST("/:id", h.Record)
			req := httptest.NewRequest("POST", "/"+id.String(), strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
			if tc.name == "valid" && (stub.tenant != tenant || stub.actor != actor || stub.id != id) {
				t.Fatal("trusted scope lost")
			}
		})
	}
}
