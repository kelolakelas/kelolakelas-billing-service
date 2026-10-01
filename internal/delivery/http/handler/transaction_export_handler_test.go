package handler

import (
	"encoding/csv"
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

// The date basis is opt-in: omitting date_by must keep filtering on the
// creation date exactly as before, so existing callers see no change.
func TestListWithoutDateByKeepsTheCreatedAtBasis(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callList(t, stub, "/api/v1/billing/transactions?date_from=2026-09-01&date_to=2026-09-30")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if stub.lastQuery.DateBy != "" {
		t.Fatalf("usecase received date_by %q, want the empty historical default", stub.lastQuery.DateBy)
	}
}

func TestListForwardsAnExplicitPaidAtBasis(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callList(t, stub, "/api/v1/billing/transactions?date_from=2026-09-01&date_to=2026-09-30&date_by=paid_at")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if stub.lastQuery.DateBy != domain.TransactionDateByPaidAt {
		t.Fatalf("usecase received date_by %q, want %q", stub.lastQuery.DateBy, domain.TransactionDateByPaidAt)
	}
}

// An unknown basis must be rejected: silently falling back to created_at
// would return rows the caller did not ask for while looking like an empty
// paid-date range.
func TestListRejectsAnUnknownDateBasis(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callList(t, stub, "/api/v1/billing/transactions?date_by=updated_at")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	if stub.lastQuery.DateBy != "" {
		t.Fatalf("usecase was called with date_by %q, want the request rejected first", stub.lastQuery.DateBy)
	}
}

func callExport(t *testing.T, stub *transactionUsecaseStub, tenantID string, isParent bool, target string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/billing/transactions/export", func(c *gin.Context) {
		c.Set("is_parent", isParent)
		c.Set("tenant_id", tenantID)
		c.Set("user_id", uuid.NewString())
		c.Next()
	}, NewTransactionHandler(stub, nil).Export)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

// The default export must reconcile with the sales summary out of the box:
// paid transactions on the paid date over the last 30 UTC days.
func TestExportDefaultsToPaidStatusAndPaidAtBasis(t *testing.T) {
	tenantID := uuid.NewString()
	stub := &transactionUsecaseStub{}
	recorder := callExport(t, stub, tenantID, false, "/api/v1/billing/transactions/export")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if stub.lastExportTenant.String() != tenantID {
		t.Fatalf("tenant=%s, want the token tenant %s", stub.lastExportTenant, tenantID)
	}
	if stub.lastExportQuery.Status != domain.TransactionStatusPaid {
		t.Fatalf("status=%q, want the paid default", stub.lastExportQuery.Status)
	}
	if stub.lastExportQuery.DateBy != domain.TransactionDateByPaidAt {
		t.Fatalf("date_by=%q, want the paid_at default", stub.lastExportQuery.DateBy)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if !stub.lastExportQuery.DateFrom.Equal(today.AddDate(0, 0, -29)) || !stub.lastExportQuery.DateTo.Equal(today) {
		t.Fatalf("range=[%s,%s], want the last 30 UTC days", stub.lastExportQuery.DateFrom, stub.lastExportQuery.DateTo)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("content-type=%q, want CSV", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.HasPrefix(got, `attachment; filename="transactions-`) || !strings.HasSuffix(got, `.csv"`) {
		t.Fatalf("content-disposition=%q, want a dated attachment filename", got)
	}
	records, err := csv.NewReader(strings.NewReader(recorder.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("body is not valid CSV: %v", err)
	}
	if len(records) != 1 || len(records[0]) != len(domain.TransactionExportColumns) {
		t.Fatalf("records=%v, want a header-only CSV for an empty range", records)
	}
}

func TestExportForwardsExplicitFilters(t *testing.T) {
	tenantID := uuid.NewString()
	studentID := uuid.NewString()
	paidAt := time.Date(2026, time.September, 14, 3, 30, 0, 0, time.UTC)
	stub := &transactionUsecaseStub{exportTxns: []domain.Transaction{{
		MerchantOrderID: "=HYPERLINK(\"http://evil.example\")",
		Status:          domain.TransactionStatusPaid,
		StudentID:       uuid.MustParse(studentID),
		EnrollmentID:    uuid.New(),
		Currency:        "IDR",
		SubtotalAmount:  250000, DiscountAmount: 25000, GrossAmount: 225000,
		PlatformFee: 20000, PaymentGatewayFee: 5000, NetAmount: 200000,
		PaidAt:    &paidAt,
		CreatedAt: time.Date(2026, time.September, 13, 17, 0, 0, 0, time.UTC),
	}}}
	target := "/api/v1/billing/transactions/export?status=paid&student_id=" + studentID +
		"&date_from=2026-09-01&date_to=2026-09-30&date_by=paid_at"
	recorder := callExport(t, stub, tenantID, false, target)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if stub.lastExportQuery.StudentID == nil || stub.lastExportQuery.StudentID.String() != studentID {
		t.Fatalf("student filter=%v, want %s", stub.lastExportQuery.StudentID, studentID)
	}
	// The injected order id must reach the spreadsheet neutralized: a leading
	// quote turns the payload into text instead of a live formula.
	if !strings.Contains(recorder.Body.String(), "'=HYPERLINK") {
		t.Fatalf("body does not neutralize the formula payload:\n%s", recorder.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(recorder.Body.String())).ReadAll()
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%v err=%v, want header + one row", records, err)
	}
}

func TestExportRejectsInvalidRanges(t *testing.T) {
	for name, query := range map[string]string{
		"from after to":             "?date_from=2026-09-30&date_to=2026-09-01",
		"367 days":                  "?date_from=2025-01-01&date_to=2026-01-02",
		"malformed from":            "?date_from=2026-9-1&date_to=2026-09-30",
		"timestamp instead of date": "?date_from=2026-09-01T00:00:00Z&date_to=2026-09-30",
		"impossible date":           "?date_from=2026-02-30&date_to=2026-03-01",
		"empty to":                  "?date_from=2026-09-01&date_to=",
		"unknown status":            "?status=not-a-status",
		"unknown date basis":        "?date_by=updated_at",
		"malformed student id":      "?student_id=not-a-uuid",
	} {
		t.Run(name, func(t *testing.T) {
			stub := &transactionUsecaseStub{}
			recorder := callExport(t, stub, uuid.NewString(), false, "/api/v1/billing/transactions/export"+query)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", recorder.Code, recorder.Body.String())
			}
			if stub.calls != 0 {
				t.Fatal("an invalid export request reached the usecase")
			}
		})
	}
}

func TestExportAcceptsTheBoundaryRanges(t *testing.T) {
	for name, query := range map[string]string{
		"single day":                "?date_from=2026-09-27&date_to=2026-09-27",
		"366 days across leap year": "?date_from=2027-03-01&date_to=2028-02-29",
		"explicit created_at basis": "?date_from=2026-09-01&date_to=2026-09-30&date_by=created_at",
		"explicit other status":     "?status=pending&date_from=2026-09-01&date_to=2026-09-30",
	} {
		t.Run(name, func(t *testing.T) {
			stub := &transactionUsecaseStub{}
			recorder := callExport(t, stub, uuid.NewString(), false, "/api/v1/billing/transactions/export"+query)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
			}
			if stub.calls != 1 {
				t.Fatal("a valid export request did not reach the usecase")
			}
		})
	}
}

// Like the sales summary, the export is a tenant-member read: a parent token
// is refused before the usecase runs, even with a tenant claim attached.
func TestExportRefusesParents(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callExport(t, stub, uuid.NewString(), true, "/api/v1/billing/transactions/export?date_from=2026-09-01&date_to=2026-09-30")

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
	if stub.calls != 0 {
		t.Fatal("a parent export request reached the usecase")
	}
}

func TestExportReportsUsecaseFailureWithoutLeakingDetails(t *testing.T) {
	stub := &transactionUsecaseStub{exportErr: errors.New("pq: connection refused")}
	recorder := callExport(t, stub, uuid.NewString(), false, "/api/v1/billing/transactions/export?date_from=2026-09-01&date_to=2026-09-30")

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want 500", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "pq: connection refused") {
		t.Fatalf("body leaks the internal error: %s", recorder.Body.String())
	}
}
