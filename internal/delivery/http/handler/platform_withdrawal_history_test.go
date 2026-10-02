package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type historyUsecaseStub struct {
	requested, decided int
	query              domain.WithdrawalQuery
}

func (s *historyUsecaseStub) ListRequested(_ context.Context, q domain.WithdrawalQuery) (*domain.PlatformWithdrawalListResponse, error) {
	s.requested++
	s.query = q
	return &domain.PlatformWithdrawalListResponse{Items: []domain.PlatformWithdrawalResponse{}}, nil
}
func (s *historyUsecaseStub) ListDecided(_ context.Context, q domain.WithdrawalQuery) (*domain.PlatformWithdrawalListResponse, error) {
	s.decided++
	s.query = q
	now := time.Now()
	id := uuid.New()
	ref := "transfer-123"
	return &domain.PlatformWithdrawalListResponse{Items: []domain.PlatformWithdrawalResponse{{WithdrawalResponse: domain.WithdrawalResponse{ID: id, Status: domain.WithdrawalStatusPaid, AccountNumber: "12345678", DecidedBy: &id, DecidedAt: &now, TransferReference: &ref}, TenantID: uuid.New()}}}, nil
}
func (s *historyUsecaseStub) Decide(context.Context, uuid.UUID, uuid.UUID, string, string) (*domain.PlatformWithdrawalResponse, error) {
	return nil, nil
}

func TestKEL163PlatformHistorySelectionAndValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &historyUsecaseStub{}
	r := gin.New()
	r.GET("/api/v1/platform/withdrawals", NewPlatformWithdrawalHandler(s).ListRequested)
	call := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		return w
	}
	for _, target := range []string{"?status=decided&page=2&page_size=1", "?page=3", "?status=invalid", "?status=", "?status=decided&page=0", "?status=decided&page_size=101", "?status=decided&page_size=oops"} {
		w := call("/api/v1/platform/withdrawals" + target)
		if target == "?status=decided&page=2&page_size=1" {
			if w.Code != 200 || s.decided != 1 || s.query.Page != 2 || s.query.PageSize != 1 {
				t.Fatalf("history %s: code=%d stub=%+v", target, w.Code, s)
			}
			var body struct {
				Data domain.PlatformWithdrawalListResponse `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			item := body.Data.Items[0]
			if item.Status != domain.WithdrawalStatusPaid || item.DecidedAt == nil || item.DecidedBy == nil || item.TransferReference == nil || item.AccountNumber != "12345678" || item.TenantID == uuid.Nil {
				t.Fatalf("incomplete decision: %+v", item)
			}
		} else if target == "?page=3" {
			if w.Code != 200 || s.requested != 1 || s.query.Page != 3 || s.query.PageSize != 20 {
				t.Fatalf("default queue %d %+v", w.Code, s)
			}
		} else if w.Code != 400 || s.decided != 1 || s.requested != 1 {
			t.Fatalf("invalid %s: code=%d stub=%+v", target, w.Code, s)
		}
	}
}
