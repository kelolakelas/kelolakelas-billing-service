package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// The operator endpoints are internal-only, so the acceptance check is the whole route:
// the credential middleware has to guard them and the response has to carry the status
// filter and the moved-row count. This test therefore builds the same group the server
// builds instead of calling the handlers directly.
func newReconciliationRouter(t *testing.T, stub *reconciliationUsecaseStub, credential string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewReconciliationHandler(stub)
	internal := router.Group("/internal/billing")
	internal.Use(middleware.InternalServiceAuth(credential))
	internal.GET("/reconciliations", handler.ListReconciliations)
	internal.POST("/reconciliations/requeue", handler.RequeueTerminalFailedReconciliations)
	return router
}

func TestReconciliationRoutesRejectMissingInternalCredential(t *testing.T) {
	stub := &reconciliationUsecaseStub{list: &domain.ReconciliationListResponse{}}
	router := newReconciliationRouter(t, stub, "internal-secret")

	for _, test := range []struct {
		method string
		target string
	}{
		{http.MethodGet, "/internal/billing/reconciliations"},
		{http.MethodPost, "/internal/billing/reconciliations/requeue"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(test.method, test.target, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status=%d, want 401 without the internal credential", test.method, test.target, recorder.Code)
		}
	}
	if stub.listCalls != 0 || stub.requeueCalls != 0 {
		t.Fatalf("list=%d requeue=%d, want the usecase unreachable without the credential", stub.listCalls, stub.requeueCalls)
	}
}

// The two acceptance criteria end to end: a permanent failure is listed by its status, and
// re-drive reports how many rows it moved.
func TestReconciliationRoutesListAndRequeueWithInternalCredential(t *testing.T) {
	transactionID, enrollmentID := uuid.New(), uuid.New()
	stub := &reconciliationUsecaseStub{
		list: &domain.ReconciliationListResponse{Items: []domain.PaymentReconciliation{{
			ID:            uuid.New(),
			TransactionID: transactionID,
			EnrollmentID:  enrollmentID,
			Kind:          domain.ReconciliationKindActivation,
			Status:        domain.ReconciliationStatusTerminalFailed,
			AttemptCount:  3,
		}}},
		requeue: &domain.ReconciliationRequeueResponse{Requeued: 1, RequeuedAt: time.Date(2026, time.September, 21, 10, 0, 0, 0, time.UTC)},
	}
	router := newReconciliationRouter(t, stub, "internal-secret")

	listRecorder := httptest.NewRecorder()
	listRequest := httptest.NewRequest(http.MethodGet, "/internal/billing/reconciliations?status="+domain.ReconciliationStatusTerminalFailed, nil)
	listRequest.Header.Set("X-Internal-Service-Credential", "internal-secret")
	router.ServeHTTP(listRecorder, listRequest)

	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status=%d, want 200", listRecorder.Code)
	}
	if stub.lastStatusSeen != domain.ReconciliationStatusTerminalFailed {
		t.Fatalf("status=%q, want the requested filter", stub.lastStatusSeen)
	}
	var listPayload struct {
		Data struct {
			Items []struct {
				TransactionID string `json:"transaction_id"`
				EnrollmentID  string `json:"enrollment_id"`
				Status        string `json:"status"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listPayload.Data.Items) != 1 {
		t.Fatalf("items=%d, want 1", len(listPayload.Data.Items))
	}
	item := listPayload.Data.Items[0]
	if item.TransactionID != transactionID.String() || item.EnrollmentID != enrollmentID.String() {
		t.Fatalf("item=%+v, want both correlation ids so the operator can trace the job", item)
	}
	if item.Status != domain.ReconciliationStatusTerminalFailed {
		t.Fatalf("status=%q, want terminal_failed", item.Status)
	}

	requeueRecorder := httptest.NewRecorder()
	requeueRequest := httptest.NewRequest(http.MethodPost, "/internal/billing/reconciliations/requeue", nil)
	requeueRequest.Header.Set("X-Internal-Service-Credential", "internal-secret")
	router.ServeHTTP(requeueRecorder, requeueRequest)

	if requeueRecorder.Code != http.StatusOK {
		t.Fatalf("requeue status=%d, want 200", requeueRecorder.Code)
	}
	var requeuePayload struct {
		Data struct {
			Requeued int64 `json:"requeued"`
		} `json:"data"`
	}
	if err := json.Unmarshal(requeueRecorder.Body.Bytes(), &requeuePayload); err != nil {
		t.Fatalf("decode requeue response: %v", err)
	}
	if requeuePayload.Data.Requeued != 1 {
		t.Fatalf("requeued=%d, want 1", requeuePayload.Data.Requeued)
	}
}
