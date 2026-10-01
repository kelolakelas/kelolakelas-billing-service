package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type withdrawalUsecaseStub struct {
	requested *domain.WithdrawalResponse
	cancelled *domain.WithdrawalResponse
	found     *domain.WithdrawalResponse
	list      *domain.WithdrawalListResponse
	err       error
	tenant    uuid.UUID
	id        uuid.UUID
	input     domain.RequestWithdrawalInput
	query     domain.WithdrawalQuery
	calls     int
}

func (s *withdrawalUsecaseStub) RequestWithdrawal(_ context.Context, tenantID uuid.UUID, input domain.RequestWithdrawalInput) (*domain.WithdrawalResponse, error) {
	s.calls++
	s.tenant, s.input = tenantID, input
	return s.requested, s.err
}
func (s *withdrawalUsecaseStub) CancelWithdrawal(_ context.Context, tenantID, id uuid.UUID) (*domain.WithdrawalResponse, error) {
	s.calls++
	s.tenant, s.id = tenantID, id
	return s.cancelled, s.err
}
func (s *withdrawalUsecaseStub) GetWithdrawal(_ context.Context, tenantID, id uuid.UUID) (*domain.WithdrawalResponse, error) {
	s.calls++
	s.tenant, s.id = tenantID, id
	return s.found, s.err
}
func (s *withdrawalUsecaseStub) ListWithdrawals(_ context.Context, tenantID uuid.UUID, query domain.WithdrawalQuery) (*domain.WithdrawalListResponse, error) {
	s.calls++
	s.tenant, s.query = tenantID, query
	return s.list, s.err
}

func serveWithdrawal(t *testing.T, stub *withdrawalUsecaseStub, method, target, tenantID string, isParent bool, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewWithdrawalHandler(stub)
	r := gin.New()
	contextMiddleware := func(c *gin.Context) {
		c.Set("tenant_id", tenantID)
		c.Set("is_parent", isParent)
		c.Next()
	}
	r.POST("/api/v1/billing/withdrawals", contextMiddleware, h.RequestWithdrawal)
	r.GET("/api/v1/billing/withdrawals", contextMiddleware, h.ListWithdrawals)
	r.GET("/api/v1/billing/withdrawals/:id", contextMiddleware, h.GetWithdrawal)
	r.DELETE("/api/v1/billing/withdrawals/:id", contextMiddleware, h.CancelWithdrawal)
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	return recorder
}

func TestWithdrawalHandlerRequestUsesTokenTenantAndValidatesBody(t *testing.T) {
	tenant := uuid.New()
	account := uuid.New()
	stub := &withdrawalUsecaseStub{requested: &domain.WithdrawalResponse{ID: uuid.New(), Amount: 60000, Status: domain.WithdrawalStatusRequested}}
	payload := `{"amount":60000,"idempotency_key":"request-1","bank_account_id":"` + account.String() + `"}`
	got := serveWithdrawal(t, stub, http.MethodPost, "/api/v1/billing/withdrawals?tenant_id="+uuid.NewString(), tenant.String(), false, payload)
	if got.Code != http.StatusCreated || stub.tenant != tenant || stub.input.Amount != 60000 || stub.input.IdempotencyKey != "request-1" || stub.input.BankAccountID == nil || *stub.input.BankAccountID != account {
		t.Fatalf("status=%d tenant=%s input=%+v, want request from token tenant", got.Code, stub.tenant, stub.input)
	}
	var response struct {
		Data domain.WithdrawalResponse `json:"data"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil || response.Data.ID != stub.requested.ID {
		t.Fatalf("response=%s err=%v, want request data", got.Body.String(), err)
	}
	for name, body := range map[string]string{
		"missing key":     `{"amount":60000}`,
		"invalid account": `{"amount":60000,"idempotency_key":"request-2","bank_account_id":"bad"}`,
		"zero amount":     `{"amount":0,"idempotency_key":"request-3"}`,
	} {
		t.Run(name, func(t *testing.T) {
			before := stub.calls
			got := serveWithdrawal(t, stub, http.MethodPost, "/api/v1/billing/withdrawals", tenant.String(), false, body)
			if got.Code != http.StatusBadRequest || stub.calls != before {
				t.Fatalf("status=%d calls=%d, want 400 without usecase call", got.Code, stub.calls)
			}
		})
	}
}

func TestWithdrawalHandlerRefusesParentAndInvalidTenant(t *testing.T) {
	stub := &withdrawalUsecaseStub{}
	id := uuid.NewString()
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/billing/withdrawals"},
		{http.MethodGet, "/api/v1/billing/withdrawals"},
		{http.MethodGet, "/api/v1/billing/withdrawals/" + id},
		{http.MethodDelete, "/api/v1/billing/withdrawals/" + id},
	} {
		if got := serveWithdrawal(t, stub, tc.method, tc.path, uuid.NewString(), true, ""); got.Code != http.StatusForbidden {
			t.Fatalf("parent %s %s status=%d, want 403", tc.method, tc.path, got.Code)
		}
		if got := serveWithdrawal(t, stub, tc.method, tc.path, "not-a-uuid", false, ""); got.Code != http.StatusUnauthorized {
			t.Fatalf("invalid tenant %s %s status=%d, want 401", tc.method, tc.path, got.Code)
		}
	}
	if stub.calls != 0 {
		t.Fatalf("unauthorized requests reached usecase: %d", stub.calls)
	}
}

func TestWithdrawalHandlerCancelMapsErrors(t *testing.T) {
	id := uuid.NewString()
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"missing or foreign": {domain.ErrWithdrawalNotFound, http.StatusNotFound},
		"processing":         {domain.ErrWithdrawalInvalidState, http.StatusConflict},
		"storage failure":    {errors.New("private driver details"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &withdrawalUsecaseStub{err: tc.err}
			got := serveWithdrawal(t, stub, http.MethodDelete, "/api/v1/billing/withdrawals/"+id, uuid.NewString(), false, "")
			if got.Code != tc.want || strings.Contains(got.Body.String(), "private driver details") {
				t.Fatalf("status=%d body=%s, want %d sanitized", got.Code, got.Body.String(), tc.want)
			}
		})
	}
	stub := &withdrawalUsecaseStub{}
	if got := serveWithdrawal(t, stub, http.MethodDelete, "/api/v1/billing/withdrawals/not-a-uuid", uuid.NewString(), false, ""); got.Code != http.StatusBadRequest || stub.calls != 0 {
		t.Fatalf("invalid withdrawal id status=%d calls=%d, want 400", got.Code, stub.calls)
	}
}

func TestWithdrawalHandlerListsHistoryAndValidatesPagination(t *testing.T) {
	tenant := uuid.New()
	stub := &withdrawalUsecaseStub{list: &domain.WithdrawalListResponse{}}
	got := serveWithdrawal(t, stub, http.MethodGet, "/api/v1/billing/withdrawals?page=2&page_size=5&tenant_id="+uuid.NewString(), tenant.String(), false, "")
	if got.Code != http.StatusOK || stub.tenant != tenant || stub.query.Page != 2 || stub.query.PageSize != 5 || !strings.Contains(got.Body.String(), `"items":[]`) {
		t.Fatalf("status=%d tenant=%s query=%+v body=%s", got.Code, stub.tenant, stub.query, got.Body.String())
	}
	for _, query := range []string{"page=0", "page_size=101", "page=x"} {
		before := stub.calls
		got := serveWithdrawal(t, stub, http.MethodGet, "/api/v1/billing/withdrawals?"+query, tenant.String(), false, "")
		if got.Code != http.StatusBadRequest || stub.calls != before {
			t.Fatalf("query=%s status=%d calls=%d, want 400", query, got.Code, stub.calls)
		}
	}
}
