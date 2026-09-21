package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// reconciliationUsecaseStub returns a canned outcome so the tests can prove the handler
// maps each documented failure onto its status code without a store.
type reconciliationUsecaseStub struct {
	list           *domain.ReconciliationListResponse
	listErr        error
	requeue        *domain.ReconciliationRequeueResponse
	requeueErr     error
	listCalls      int
	requeueCalls   int
	lastStatusSeen string
}

func (s *reconciliationUsecaseStub) ListReconciliations(_ context.Context, status string) (*domain.ReconciliationListResponse, error) {
	s.listCalls++
	s.lastStatusSeen = status
	return s.list, s.listErr
}

func (s *reconciliationUsecaseStub) RequeueTerminalFailedReconciliations(context.Context) (*domain.ReconciliationRequeueResponse, error) {
	s.requeueCalls++
	return s.requeue, s.requeueErr
}

func callListReconciliations(t *testing.T, stub *reconciliationUsecaseStub, target string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/internal/billing/reconciliations", NewReconciliationHandler(stub).ListReconciliations)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func callRequeueReconciliations(t *testing.T, stub *reconciliationUsecaseStub) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/internal/billing/reconciliations/requeue", NewReconciliationHandler(stub).RequeueTerminalFailedReconciliations)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/billing/reconciliations/requeue", nil))
	return recorder
}

// The status filter an operator types has to reach the usecase, otherwise the endpoint
// silently answers with every row and the filter looks broken.
func TestListReconciliationsHandlerForwardsStatusFilter(t *testing.T) {
	stub := &reconciliationUsecaseStub{list: &domain.ReconciliationListResponse{}}

	recorder := callListReconciliations(t, stub, "/internal/billing/reconciliations?status=terminal_failed")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", recorder.Code)
	}
	if stub.lastStatusSeen != domain.ReconciliationStatusTerminalFailed {
		t.Fatalf("status=%q, want %q", stub.lastStatusSeen, domain.ReconciliationStatusTerminalFailed)
	}
}

// An unknown status is the operator's mistake and has to read as 400, not as a 500 that
// hides which side got it wrong.
func TestListReconciliationsHandlerRejectsUnknownStatus(t *testing.T) {
	stub := &reconciliationUsecaseStub{listErr: domain.ErrInvalidReconciliationStatus}

	recorder := callListReconciliations(t, stub, "/internal/billing/reconciliations?status=nope")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

// A service started without a reconciliation store answers 503 so an operator can tell
// misconfiguration apart from a real failure.
func TestListReconciliationsHandlerReportsMissingStore(t *testing.T) {
	stub := &reconciliationUsecaseStub{listErr: domain.ErrReconciliationUnavailable}

	recorder := callListReconciliations(t, stub, "/internal/billing/reconciliations")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", recorder.Code)
	}
}

// Any other failure is a server-side problem and must not leak the store error to the
// caller.
func TestListReconciliationsHandlerHidesStoreErrors(t *testing.T) {
	stub := &reconciliationUsecaseStub{listErr: errors.New("connection refused to db host")}

	recorder := callListReconciliations(t, stub, "/internal/billing/reconciliations")

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", recorder.Code)
	}
	if body := recorder.Body.String(); body == "" || strings.Contains(body, "connection refused") {
		t.Fatalf("body=%q, want a generic message", body)
	}
}

// The re-drive answers with the count the database actually moved, so an operator can
// confirm the call did something.
func TestRequeueReconciliationsHandlerReportsCount(t *testing.T) {
	stub := &reconciliationUsecaseStub{requeue: &domain.ReconciliationRequeueResponse{
		Requeued: 2, RequeuedAt: time.Date(2026, time.September, 21, 10, 0, 0, 0, time.UTC),
	}}

	recorder := callRequeueReconciliations(t, stub)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", recorder.Code)
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Requeued int64 `json:"requeued"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "success" || payload.Data.Requeued != 2 {
		t.Fatalf("payload=%+v, want success with requeued=2", payload)
	}
}

// Without a store the re-drive reports 503 rather than an empty success that would read as
// "nothing needed retrying".
func TestRequeueReconciliationsHandlerReportsMissingStore(t *testing.T) {
	stub := &reconciliationUsecaseStub{requeueErr: domain.ErrReconciliationUnavailable}

	recorder := callRequeueReconciliations(t, stub)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", recorder.Code)
	}
}
