package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type summaryCaller struct {
	tenantID string
	isParent bool
}

func callSalesSummary(t *testing.T, stub *transactionUsecaseStub, caller summaryCaller, target string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/billing/transactions/summary", func(c *gin.Context) {
		c.Set("tenant_id", caller.tenantID)
		c.Set("is_parent", caller.isParent)
		c.Next()
	}, NewTransactionHandler(stub, nil).SalesSummary)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

// The half-open [from, to+1 day) interval is what makes `to` inclusive for every paid_at
// on that UTC day, and the tenant must come from the verified token context only.
func TestSalesSummaryForwardsTokenTenantAndInclusiveUTCRange(t *testing.T) {
	tenantID := uuid.New()
	stub := &transactionUsecaseStub{summary: []domain.SalesSummary{{Currency: "IDR", TransactionCount: 2, GrossAmount: 300000, NetAmount: 270000}}}
	recorder := callSalesSummary(t, stub, summaryCaller{tenantID: tenantID.String()},
		"/api/v1/billing/transactions/summary?from=2026-09-01&to=2026-09-30&tenant_id="+uuid.NewString())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if stub.summaryTenant != tenantID {
		t.Fatalf("tenant=%s, want the token tenant %s", stub.summaryTenant, tenantID)
	}
	wantFrom := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	wantUntil := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	if !stub.summaryFrom.Equal(wantFrom) || !stub.summaryUntil.Equal(wantUntil) {
		t.Fatalf("range=[%s,%s), want [%s,%s)", stub.summaryFrom, stub.summaryUntil, wantFrom, wantUntil)
	}
	var payload struct {
		Data domain.SalesSummaryResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Data.From != "2026-09-01" || payload.Data.To != "2026-09-30" || len(payload.Data.Totals) != 1 ||
		payload.Data.Totals[0] != stub.summary[0] {
		t.Fatalf("payload=%+v", payload.Data)
	}
}

func TestSalesSummaryDefaultsToTheLastThirtyUTCDays(t *testing.T) {
	stub := &transactionUsecaseStub{summary: []domain.SalesSummary{}}
	before := time.Now().UTC().Truncate(24 * time.Hour)
	recorder := callSalesSummary(t, stub, summaryCaller{tenantID: uuid.NewString()}, "/api/v1/billing/transactions/summary")
	after := time.Now().UTC().Truncate(24 * time.Hour)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// Tolerate the test straddling UTC midnight.
	for _, today := range []time.Time{before, after} {
		if stub.summaryFrom.Equal(today.AddDate(0, 0, -29)) && stub.summaryUntil.Equal(today.AddDate(0, 0, 1)) {
			var payload struct {
				Data struct {
					Totals []domain.SalesSummary `json:"totals"`
				} `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || payload.Data.Totals == nil {
				t.Fatalf("empty totals must serialize as [], body=%s", recorder.Body.String())
			}
			return
		}
	}
	t.Fatalf("default range=[%s,%s), want 30 UTC days ending today", stub.summaryFrom, stub.summaryUntil)
}

func TestSalesSummaryRejectsInvalidRanges(t *testing.T) {
	for name, query := range map[string]string{
		"from after to":             "?from=2026-09-30&to=2026-09-01",
		"367 days":                  "?from=2025-01-01&to=2026-01-02",
		"malformed from":            "?from=2026-9-1&to=2026-09-30",
		"timestamp instead of date": "?from=2026-09-01T00:00:00Z&to=2026-09-30",
		"impossible date":           "?from=2026-02-30&to=2026-03-01",
		"empty to":                  "?from=2026-09-01&to=",
		"to before default from":    "?to=2000-01-01",
	} {
		t.Run(name, func(t *testing.T) {
			stub := &transactionUsecaseStub{}
			recorder := callSalesSummary(t, stub, summaryCaller{tenantID: uuid.NewString()}, "/api/v1/billing/transactions/summary"+query)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", recorder.Code, recorder.Body.String())
			}
			if stub.calls != 0 {
				t.Fatal("an invalid range reached the usecase")
			}
		})
	}
}

func TestSalesSummaryAcceptsTheBoundaryRanges(t *testing.T) {
	for name, query := range map[string]string{
		"single day":                "?from=2026-09-27&to=2026-09-27",
		"366 days across leap year": "?from=2027-03-01&to=2028-02-29",
	} {
		t.Run(name, func(t *testing.T) {
			stub := &transactionUsecaseStub{}
			recorder := callSalesSummary(t, stub, summaryCaller{tenantID: uuid.NewString()}, "/api/v1/billing/transactions/summary"+query)
			if recorder.Code != http.StatusOK || stub.calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", recorder.Code, stub.calls, recorder.Body.String())
			}
		})
	}
}

// Defense in depth behind the route middleware: parents and tokens without a tenant never query.
func TestSalesSummaryRefusesCallersWithoutTenantMemberContext(t *testing.T) {
	for name, test := range map[string]struct {
		caller summaryCaller
		want   int
	}{
		"parent with tenant claim": {caller: summaryCaller{tenantID: uuid.NewString(), isParent: true}, want: http.StatusForbidden},
		"missing tenant":           {caller: summaryCaller{}, want: http.StatusUnauthorized},
		"nil tenant":               {caller: summaryCaller{tenantID: uuid.Nil.String()}, want: http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &transactionUsecaseStub{}
			recorder := callSalesSummary(t, stub, test.caller, "/api/v1/billing/transactions/summary")
			if recorder.Code != test.want || stub.calls != 0 {
				t.Fatalf("status=%d calls=%d, want %d without a usecase call", recorder.Code, stub.calls, test.want)
			}
		})
	}
}

func TestSalesSummaryReportsRepositoryFailure(t *testing.T) {
	stub := &transactionUsecaseStub{err: errors.New("database is unavailable")}
	recorder := callSalesSummary(t, stub, summaryCaller{tenantID: uuid.NewString()}, "/api/v1/billing/transactions/summary")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want 500", recorder.Code, recorder.Body.String())
	}
}
