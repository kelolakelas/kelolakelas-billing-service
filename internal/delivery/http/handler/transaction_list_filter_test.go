package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// callList runs the authenticated list endpoint with the tenant supplied by the auth
// middleware. The middleware is replaced by the context values the handler reads, which
// keeps this test focused on the status filter.
func callList(t *testing.T, stub *transactionUsecaseStub, target string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/billing/transactions", func(c *gin.Context) {
		c.Set("is_parent", false)
		c.Set("tenant_id", uuid.NewString())
		c.Set("user_id", uuid.NewString())
		NewTransactionHandler(stub, nil).List(c)
	})

	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// The list filter is the only way an operator can find a transaction parked in `creating`,
// which is exactly the state this change makes recoverable. Every status the code can write
// has to be accepted by the endpoint, otherwise the stuck rows stay invisible in the UI.
func TestListAcceptsEveryStatusTheCodeCanWrite(t *testing.T) {
	for _, status := range domain.TransactionStatusFilterValues() {
		t.Run(status, func(t *testing.T) {
			stub := &transactionUsecaseStub{}
			recorder := callList(t, stub, "/api/v1/billing/transactions?status="+status)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want 200 for status %q", recorder.Code, recorder.Body.String(), status)
			}
			if stub.lastQuery.Status != status {
				t.Fatalf("usecase received status %q, want %q", stub.lastQuery.Status, status)
			}
		})
	}
}

// The `creating` value was missing from the filter before this change, so it is asserted by
// name as well: a regression there would silently hide the transactions being recovered.
func TestListAcceptsTheCreatingStatusFilter(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callList(t, stub, "/api/v1/billing/transactions?status="+domain.TransactionStatusCreating)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if stub.lastQuery.Status != domain.TransactionStatusCreating {
		t.Fatalf("usecase received status %q, want %q", stub.lastQuery.Status, domain.TransactionStatusCreating)
	}
}

// An unknown value must still be rejected, because forwarding it would quietly return an
// empty list and look like "no stuck transactions" to the caller.
func TestListRejectsUnknownStatusFilter(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callList(t, stub, "/api/v1/billing/transactions?status=not-a-status")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	if stub.lastQuery.Status != "" {
		t.Fatalf("usecase was called with status %q, want the request rejected first", stub.lastQuery.Status)
	}
}

// An omitted status must keep meaning "no filter" rather than being treated as an invalid
// value, so the default listing is unchanged.
func TestListWithoutStatusFilterIsUnchanged(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callList(t, stub, "/api/v1/billing/transactions")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if stub.lastQuery.Status != "" {
		t.Fatalf("usecase received status %q, want an empty filter", stub.lastQuery.Status)
	}
}
