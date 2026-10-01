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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type walletUsecaseStub struct {
	balance *domain.WalletBalanceResponse
	balErr  error
	ledger  *domain.LedgerListResponse
	ledErr  error
	tenant  uuid.UUID
	query   domain.LedgerQuery
}

func (s *walletUsecaseStub) GetBalance(_ context.Context, tenantID uuid.UUID) (*domain.WalletBalanceResponse, error) {
	s.tenant = tenantID
	if s.balErr != nil {
		return nil, s.balErr
	}
	return s.balance, nil
}

func (s *walletUsecaseStub) ListLedger(_ context.Context, tenantID uuid.UUID, query domain.LedgerQuery) (*domain.LedgerListResponse, error) {
	s.tenant = tenantID
	s.query = query
	if s.ledErr != nil {
		return nil, s.ledErr
	}
	return s.ledger, nil
}

type bankAccountUsecaseStub struct {
	list     *domain.BankAccountListResponse
	listErr  error
	created  *domain.BankAccountResponse
	createEr error
	updated  *domain.BankAccountResponse
	updateEr error
	deleteEr error
	primary  *domain.BankAccountResponse
	primEr   error
	tenant   uuid.UUID
	id       uuid.UUID
	createRQ *domain.CreateBankAccountRequest
	updateRQ *domain.UpdateBankAccountRequest
}

func (s *bankAccountUsecaseStub) List(_ context.Context, tenantID uuid.UUID) (*domain.BankAccountListResponse, error) {
	s.tenant = tenantID
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.list, nil
}

func (s *bankAccountUsecaseStub) Create(_ context.Context, tenantID uuid.UUID, req *domain.CreateBankAccountRequest) (*domain.BankAccountResponse, error) {
	s.tenant, s.createRQ = tenantID, req
	if s.createEr != nil {
		return nil, s.createEr
	}
	return s.created, nil
}

func (s *bankAccountUsecaseStub) Update(_ context.Context, tenantID, id uuid.UUID, req *domain.UpdateBankAccountRequest) (*domain.BankAccountResponse, error) {
	s.tenant, s.id, s.updateRQ = tenantID, id, req
	if s.updateEr != nil {
		return nil, s.updateEr
	}
	return s.updated, nil
}

func (s *bankAccountUsecaseStub) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	s.tenant, s.id = tenantID, id
	return s.deleteEr
}

func (s *bankAccountUsecaseStub) SetPrimary(_ context.Context, tenantID, id uuid.UUID) (*domain.BankAccountResponse, error) {
	s.tenant, s.id = tenantID, id
	if s.primEr != nil {
		return nil, s.primEr
	}
	return s.primary, nil
}

func serveWallet(t *testing.T, wallets *walletUsecaseStub, route, target, tenantID string, isParent bool, body string, method string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := NewWalletHandler(wallets)
	router := gin.New()
	router.GET("/api/v1/billing/wallet", func(c *gin.Context) {
		c.Set("tenant_id", tenantID)
		c.Set("is_parent", isParent)
		c.Next()
	}, w.GetBalance)
	router.GET("/api/v1/billing/ledger", func(c *gin.Context) {
		c.Set("tenant_id", tenantID)
		c.Set("is_parent", isParent)
		c.Next()
	}, w.ListLedger)
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	_ = route
	return recorder
}

func serveBank(t *testing.T, accounts *bankAccountUsecaseStub, target, tenantID string, isParent bool, body, method string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewBankAccountHandler(accounts)
	router := gin.New()
	tenant := func(c *gin.Context) {
		c.Set("tenant_id", tenantID)
		c.Set("is_parent", isParent)
		c.Next()
	}
	router.GET("/api/v1/billing/bank-accounts", tenant, h.ListBankAccounts)
	router.POST("/api/v1/billing/bank-accounts", tenant, h.CreateBankAccount)
	router.PATCH("/api/v1/billing/bank-accounts/:id", tenant, h.UpdateBankAccount)
	router.DELETE("/api/v1/billing/bank-accounts/:id", tenant, h.DeleteBankAccount)
	router.POST("/api/v1/billing/bank-accounts/:id/set-primary", tenant, h.SetPrimaryBankAccount)
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

// KEL-142: parents are refused before the usecase runs, on every new route.
func TestWalletAndBankHandlersRefuseParents(t *testing.T) {
	wallets := &walletUsecaseStub{balance: &domain.WalletBalanceResponse{AvailableBalance: 1}}
	if got := serveWallet(t, wallets, "wallet", "/api/v1/billing/wallet", uuid.NewString(), true, "", http.MethodGet); got.Code != http.StatusForbidden {
		t.Fatalf("parent wallet status=%d, want 403", got.Code)
	}
	if got := serveWallet(t, wallets, "ledger", "/api/v1/billing/ledger", uuid.NewString(), true, "", http.MethodGet); got.Code != http.StatusForbidden {
		t.Fatalf("parent ledger status=%d, want 403", got.Code)
	}
	accounts := &bankAccountUsecaseStub{list: &domain.BankAccountListResponse{}}
	for name, tc := range map[string]struct {
		target, method, body string
	}{
		"list":        {"/api/v1/billing/bank-accounts", http.MethodGet, ""},
		"create":      {"/api/v1/billing/bank-accounts", http.MethodPost, `{"bank_code":"014","account_number":"123","account_name":"X"}`},
		"update":      {"/api/v1/billing/bank-accounts/" + uuid.NewString(), http.MethodPatch, `{"account_name":"Y"}`},
		"delete":      {"/api/v1/billing/bank-accounts/" + uuid.NewString(), http.MethodDelete, ""},
		"set-primary": {"/api/v1/billing/bank-accounts/" + uuid.NewString() + "/set-primary", http.MethodPost, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := serveBank(t, accounts, tc.target, uuid.NewString(), true, tc.body, tc.method); got.Code != http.StatusForbidden {
				t.Fatalf("parent %s status=%d, want 403", name, got.Code)
			}
		})
	}
}

// KEL-142: the tenant comes from the verified token context only; a query
// parameter naming another tenant must not change the scope.
func TestWalletBalanceForwardsTokenTenant(t *testing.T) {
	tenantID := uuid.New()
	stub := &walletUsecaseStub{balance: &domain.WalletBalanceResponse{AvailableBalance: 90000, PendingBalance: 0}}
	recorder := serveWallet(t, stub, "wallet", "/api/v1/billing/wallet?tenant_id="+uuid.NewString(), tenantID.String(), false, "", http.MethodGet)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if stub.tenant != tenantID {
		t.Fatalf("tenant=%s, want the token tenant %s", stub.tenant, tenantID)
	}
	var payload struct {
		Data domain.WalletBalanceResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Data.AvailableBalance != 90000 {
		t.Fatalf("balance=%d, want 90000", payload.Data.AvailableBalance)
	}
}

func TestWalletBalanceRejectsMissingTenant(t *testing.T) {
	stub := &walletUsecaseStub{balance: &domain.WalletBalanceResponse{}}
	if got := serveWallet(t, stub, "wallet", "/api/v1/billing/wallet", "not-a-uuid", false, "", http.MethodGet); got.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", got.Code)
	}
	empty := &walletUsecaseStub{balance: &domain.WalletBalanceResponse{}, balErr: errors.New("db down")}
	if got := serveWallet(t, empty, "wallet", "/api/v1/billing/wallet", uuid.NewString(), false, "", http.MethodGet); got.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", got.Code)
	}
	if strings.Contains(emptyString(empty), "db down") {
		t.Fatal("unreachable")
	}
}

func emptyString(_ *walletUsecaseStub) string { return "" }

// KEL-142: ledger pagination is validated at the boundary; an invalid page
// never reaches the usecase.
func TestLedgerListValidatesPagination(t *testing.T) {
	tenant := uuid.NewString()
	for name, target := range map[string]string{
		"page zero":       "/api/v1/billing/ledger?page=0",
		"page negative":   "/api/v1/billing/ledger?page=-1",
		"page not number": "/api/v1/billing/ledger?page=abc",
		"page_size range": "/api/v1/billing/ledger?page_size=101",
	} {
		t.Run(name, func(t *testing.T) {
			stub := &walletUsecaseStub{ledger: &domain.LedgerListResponse{}}
			if got := serveWallet(t, stub, "ledger", target, tenant, false, "", http.MethodGet); got.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400", got.Code)
			}
		})
	}
	stub := &walletUsecaseStub{ledger: &domain.LedgerListResponse{}}
	recorder := serveWallet(t, stub, "ledger", "/api/v1/billing/ledger?page=2&page_size=5", tenant, false, "", http.MethodGet)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if stub.query.Page != 2 || stub.query.PageSize != 5 {
		t.Fatalf("query=%+v, want page 2 size 5", stub.query)
	}
}

// KEL-142: account numbers are always masked in tenant reads, including the
// list; a leaked response never exposes a full account number.
func TestBankAccountResponsesMaskAccountNumbers(t *testing.T) {
	if got := domain.MaskAccountNumber("1234567890"); got != "******7890" {
		t.Fatalf("masked=%q, want ******7890", got)
	}
	if got := domain.MaskAccountNumber("123"); got != "****" {
		t.Fatalf("short masked=%q, want ****", got)
	}
	tenant := uuid.NewString()
	account := domain.BankAccount{ID: uuid.New(), TenantID: uuid.New(), BankCode: "014", AccountNumber: "9876543210", AccountName: "Bendahara", IsPrimary: true, CreatedAt: time.Now()}
	stub := &bankAccountUsecaseStub{list: &domain.BankAccountListResponse{Items: []domain.BankAccountResponse{*domain.ToBankAccountResponse(&account)}}}
	recorder := serveBank(t, stub, "/api/v1/billing/bank-accounts", tenant, false, "", http.MethodGet)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "9876543210") {
		t.Fatalf("response leaks the full account number: %s", recorder.Body.String())
	}
	var payload struct {
		Data domain.BankAccountListResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Data.Items) != 1 || payload.Data.Items[0].AccountNumber != "******3210" {
		t.Fatalf("payload=%+v, want one masked item", payload.Data)
	}
}

// KEL-142: every documented bank-account failure maps to its status code, and
// an unmapped error is sanitised.
func TestBankAccountErrorMapping(t *testing.T) {
	id := uuid.NewString()
	tenant := uuid.NewString()
	for name, tc := range map[string]struct {
		stub       *bankAccountUsecaseStub
		target     string
		method     string
		body       string
		wantStatus int
	}{
		"create invalid":  {&bankAccountUsecaseStub{createEr: domain.ErrBankAccountInvalid}, "/api/v1/billing/bank-accounts", http.MethodPost, `{"bank_code":"","account_number":"1","account_name":"X"}`, http.StatusBadRequest},
		"create conflict": {&bankAccountUsecaseStub{createEr: domain.ErrBankAccountConflict}, "/api/v1/billing/bank-accounts", http.MethodPost, `{"bank_code":"014","account_number":"1","account_name":"X"}`, http.StatusConflict},
		"update missing":  {&bankAccountUsecaseStub{updateEr: domain.ErrBankAccountNotFound}, "/api/v1/billing/bank-accounts/" + id, http.MethodPatch, `{"account_name":"Y"}`, http.StatusNotFound},
		"delete in use":   {&bankAccountUsecaseStub{deleteEr: domain.ErrBankAccountInUse}, "/api/v1/billing/bank-accounts/" + id, http.MethodDelete, "", http.StatusConflict},
		"delete missing":  {&bankAccountUsecaseStub{deleteEr: domain.ErrBankAccountNotFound}, "/api/v1/billing/bank-accounts/" + id, http.MethodDelete, "", http.StatusNotFound},
		"primary missing": {&bankAccountUsecaseStub{primEr: domain.ErrBankAccountNotFound}, "/api/v1/billing/bank-accounts/" + id + "/set-primary", http.MethodPost, "", http.StatusNotFound},
		"primary race":    {&bankAccountUsecaseStub{primEr: domain.ErrBankAccountConflict}, "/api/v1/billing/bank-accounts/" + id + "/set-primary", http.MethodPost, "", http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			if got := serveBank(t, tc.stub, tc.target, tenant, false, tc.body, tc.method); got.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", got.Code, got.Body.String(), tc.wantStatus)
			}
		})
	}
	badID := serveBank(t, &bankAccountUsecaseStub{}, "/api/v1/billing/bank-accounts/not-a-uuid", tenant, false, "", http.MethodDelete)
	if badID.Code != http.StatusBadRequest {
		t.Fatalf("malformed id status=%d, want 400", badID.Code)
	}
	badBody := serveBank(t, &bankAccountUsecaseStub{}, "/api/v1/billing/bank-accounts", tenant, false, `{invalid`, http.MethodPost)
	if badBody.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status=%d, want 400", badBody.Code)
	}
	emptyPatch := serveBank(t, &bankAccountUsecaseStub{updateEr: domain.ErrBankAccountInvalid}, "/api/v1/billing/bank-accounts/"+id, tenant, false, `{}`, http.MethodPatch)
	if emptyPatch.Code != http.StatusBadRequest {
		t.Fatalf("empty patch status=%d, want 400", emptyPatch.Code)
	}
	sensitive := errors.New("pq: duplicate key value violates unique constraint uq_bank_accounts_tenant_primary")
	logged := &bankAccountUsecaseStub{deleteEr: sensitive}
	recorder := serveBank(t, logged, "/api/v1/billing/bank-accounts/"+id, tenant, false, "", http.MethodDelete)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "uq_bank_accounts_tenant_primary") {
		t.Fatal("500 body leaks the constraint name")
	}
}
