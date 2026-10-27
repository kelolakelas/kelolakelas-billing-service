package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"gorm.io/gorm"
	"net/http/httptest"
	"strings"
	"testing"
)

type privateInvoiceStub struct {
	err   error
	calls int
}

func (s *privateInvoiceStub) ReadPrivateInvoice(_ context.Context, in domain.PrivateInvoiceScope) (*domain.PrivateInvoice, error) {
	s.calls++
	return &domain.PrivateInvoice{ID: in.TransactionID, Status: "pending"}, s.err
}
func TestPrivateInvoiceCredentialValidationAndMinimalResponse(t *testing.T) {
	scope := domain.PrivateInvoiceScope{TransactionID: uuid.New(), EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New()}
	body, _ := json.Marshal(scope)
	for _, tc := range []struct {
		name, credential, body string
		err                    error
		want                   int
	}{{"anonymous", "", string(body), nil, 401}, {"wrong", "wrong", string(body), nil, 401}, {"invalid", "internal", "{}", nil, 400}, {"missing", "internal", string(body), gorm.ErrRecordNotFound, 404}, {"outage", "internal", string(body), errors.New("secret database detail"), 503}, {"valid", "internal", string(body), nil, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			s := &privateInvoiceStub{err: tc.err}
			r := gin.New()
			r.POST("/read", middleware.InternalServiceAuth("internal"), ReadPrivateRequestInvoice(s))
			req := httptest.NewRequest("POST", "/read", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Internal-Service-Credential", tc.credential)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "secret database") || strings.Contains(w.Body.String(), "gross_amount") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if tc.want < 404 && tc.want != 200 && s.calls != 0 {
				t.Fatal("reader invoked before auth/validation")
			}
		})
	}
}
