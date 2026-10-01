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

type voucherUsecaseStub struct {
	list    *domain.VoucherListResponse
	found   *domain.VoucherResponse
	created *domain.VoucherResponse
	updated *domain.VoucherResponse
	err     error
	tenant  uuid.UUID
	id      uuid.UUID
	query   domain.VoucherQuery
	create  *domain.CreateVoucherRequest
	update  *domain.UpdateVoucherRequest
	calls   int
}

func (s *voucherUsecaseStub) List(_ context.Context, tenantID uuid.UUID, query domain.VoucherQuery) (*domain.VoucherListResponse, error) {
	s.calls++
	s.tenant, s.query = tenantID, query
	return s.list, s.err
}
func (s *voucherUsecaseStub) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.VoucherResponse, error) {
	s.calls++
	s.tenant, s.id = tenantID, id
	return s.found, s.err
}
func (s *voucherUsecaseStub) Create(_ context.Context, tenantID uuid.UUID, req *domain.CreateVoucherRequest) (*domain.VoucherResponse, error) {
	s.calls++
	s.tenant, s.create = tenantID, req
	return s.created, s.err
}
func (s *voucherUsecaseStub) Update(_ context.Context, tenantID, id uuid.UUID, req *domain.UpdateVoucherRequest) (*domain.VoucherResponse, error) {
	s.calls++
	s.tenant, s.id, s.update = tenantID, id, req
	return s.updated, s.err
}
func (s *voucherUsecaseStub) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	s.calls++
	s.tenant, s.id = tenantID, id
	return s.err
}

func serveVoucher(t *testing.T, stub *voucherUsecaseStub, method, target, tenantID string, isParent bool, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewVoucherHandler(stub)
	r := gin.New()
	contextMiddleware := func(c *gin.Context) {
		c.Set("tenant_id", tenantID)
		c.Set("is_parent", isParent)
		c.Next()
	}
	r.GET("/api/v1/billing/vouchers", contextMiddleware, h.ListVouchers)
	r.POST("/api/v1/billing/vouchers", contextMiddleware, h.CreateVoucher)
	r.GET("/api/v1/billing/vouchers/:id", contextMiddleware, h.GetVoucher)
	r.PATCH("/api/v1/billing/vouchers/:id", contextMiddleware, h.UpdateVoucher)
	r.DELETE("/api/v1/billing/vouchers/:id", contextMiddleware, h.DeleteVoucher)
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	return recorder
}

// KEL-161: the tenant claim from the token scopes every call — a query-string
// tenant_id must not override it.
func TestVoucherHandlerUsesTokenTenant(t *testing.T) {
	tenant := uuid.New()
	stub := &voucherUsecaseStub{list: &domain.VoucherListResponse{}}
	got := serveVoucher(t, stub, http.MethodGet, "/api/v1/billing/vouchers?page=2&page_size=5&tenant_id="+uuid.NewString(), tenant.String(), false, "")
	if got.Code != http.StatusOK || stub.tenant != tenant || stub.query.Page != 2 || stub.query.PageSize != 5 || !strings.Contains(got.Body.String(), `"items":[]`) {
		t.Fatalf("status=%d tenant=%s query=%+v body=%s", got.Code, stub.tenant, stub.query, got.Body.String())
	}
	for _, query := range []string{"page=0", "page_size=101", "page=x"} {
		before := stub.calls
		got := serveVoucher(t, stub, http.MethodGet, "/api/v1/billing/vouchers?"+query, tenant.String(), false, "")
		if got.Code != http.StatusBadRequest || stub.calls != before {
			t.Fatalf("query=%s status=%d calls=%d, want 400", query, got.Code, stub.calls)
		}
	}
}

// KEL-161: parents never manage tenant vouchers, and a malformed tenant
// context is 401 before the usecase runs.
func TestVoucherHandlerRefusesParentAndInvalidTenant(t *testing.T) {
	stub := &voucherUsecaseStub{}
	id := uuid.NewString()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/billing/vouchers"},
		{http.MethodPost, "/api/v1/billing/vouchers"},
		{http.MethodGet, "/api/v1/billing/vouchers/" + id},
		{http.MethodPatch, "/api/v1/billing/vouchers/" + id},
		{http.MethodDelete, "/api/v1/billing/vouchers/" + id},
	} {
		if got := serveVoucher(t, stub, tc.method, tc.path, uuid.NewString(), true, ""); got.Code != http.StatusForbidden {
			t.Fatalf("parent %s %s status=%d, want 403", tc.method, tc.path, got.Code)
		}
		if got := serveVoucher(t, stub, tc.method, tc.path, "not-a-uuid", false, ""); got.Code != http.StatusUnauthorized {
			t.Fatalf("invalid tenant %s %s status=%d, want 401", tc.method, tc.path, got.Code)
		}
	}
	if stub.calls != 0 {
		t.Fatalf("unauthorized requests reached usecase: %d", stub.calls)
	}
}

// KEL-161: duplicate codes are 409 with a clear message, invalid values 400,
// foreign or missing ids 404, and driver details never leak through 500.
func TestVoucherHandlerMapsErrors(t *testing.T) {
	id := uuid.NewString()
	tenant := uuid.NewString()
	for name, tc := range map[string]struct {
		err  error
		want int
		body string
	}{
		"missing or foreign": {domain.ErrVoucherNotFound, http.StatusNotFound, "Voucher not found"},
		"invalid request":    {domain.ErrVoucherInvalid, http.StatusBadRequest, "Invalid voucher request"},
		"duplicate code":     {domain.ErrVoucherDuplicate, http.StatusConflict, "Voucher code already exists"},
		"used voucher":       {domain.ErrVoucherInUse, http.StatusConflict, "can only be deactivated"},
		"storage failure":    {errors.New("private driver details"), http.StatusInternalServerError, "Failed to manage voucher"},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &voucherUsecaseStub{err: tc.err}
			for _, route := range []struct{ method, path string }{
				{http.MethodGet, "/api/v1/billing/vouchers/" + id},
				{http.MethodDelete, "/api/v1/billing/vouchers/" + id},
				{http.MethodGet, "/api/v1/billing/vouchers"},
			} {
				got := serveVoucher(t, stub, route.method, route.path, tenant, false, "")
				if got.Code != tc.want || !strings.Contains(got.Body.String(), tc.body) || strings.Contains(got.Body.String(), "private driver details") {
					t.Fatalf("%s status=%d body=%s, want %d with %q sanitized", route.path, got.Code, got.Body.String(), tc.want, tc.body)
				}
			}
		})
	}
	stub := &voucherUsecaseStub{}
	if got := serveVoucher(t, stub, http.MethodGet, "/api/v1/billing/vouchers/not-a-uuid", tenant, false, ""); got.Code != http.StatusBadRequest || stub.calls != 0 {
		t.Fatalf("invalid voucher id status=%d calls=%d, want 400", got.Code, stub.calls)
	}
}

// KEL-161: create answers 201 with the stored voucher; malformed JSON never
// reaches the usecase.
func TestVoucherHandlerCreateReturns201(t *testing.T) {
	tenant := uuid.New()
	stub := &voucherUsecaseStub{created: &domain.VoucherResponse{ID: uuid.New(), Code: "HEMAT10", CurrentUses: 0, IsActive: true}}
	payload := `{"code":"hemat10","discount_type":"percentage","discount_value":10}`
	got := serveVoucher(t, stub, http.MethodPost, "/api/v1/billing/vouchers?tenant_id="+uuid.NewString(), tenant.String(), false, payload)
	if got.Code != http.StatusCreated || stub.tenant != tenant || stub.create == nil || stub.create.Code != "hemat10" {
		t.Fatalf("status=%d tenant=%s create=%+v", got.Code, stub.tenant, stub.create)
	}
	var response struct {
		Data domain.VoucherResponse `json:"data"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil || response.Data.ID != stub.created.ID {
		t.Fatalf("response=%s err=%v, want created data", got.Body.String(), err)
	}
	before := stub.calls
	if got := serveVoucher(t, stub, http.MethodPost, "/api/v1/billing/vouchers", tenant.String(), false, "{bad json"); got.Code != http.StatusBadRequest || stub.calls != before {
		t.Fatalf("malformed status=%d calls=%d, want 400 without usecase call", got.Code, stub.calls)
	}
}
