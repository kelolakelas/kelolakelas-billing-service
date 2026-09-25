package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

// generateErrTransactionStub is a usecase.TransactionUsecase whose
// GenerateSubscriptionPayment fails with the canned error, so the generate
// endpoint's unmapped 500 branch can be driven without touching the shared
// success-oriented transactionUsecaseStub.
type generateErrTransactionStub struct {
	usecase.TransactionUsecase
	err error
}

func (s *generateErrTransactionStub) GenerateSubscriptionPayment(context.Context, *domain.GenerateSubscriptionPaymentRequest) (*domain.GenerateSubscriptionPaymentResponse, error) {
	return nil, s.err
}

const billingSensitiveErrorMarker = "pq: duplicate key value violates unique constraint transactions_pkey"

// billingCapturedSlogRecords routes the default slog logger into a buffer the
// test can assert on. It returns a restore function so every test puts the
// process default logger back exactly as it found it.
func billingCapturedSlogRecords(buffer *bytes.Buffer) (restore func()) {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, nil)))
	return func() { slog.SetDefault(previous) }
}

// billingFiveHundredBody keeps the response envelope shape explicit: a
// sanitised 500 must stay a {status, message, data} envelope.
type billingFiveHundredBody struct {
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

func decodeBillingFiveHundredBody(t *testing.T, recorder *httptest.ResponseRecorder) billingFiveHundredBody {
	t.Helper()
	var body billingFiveHundredBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, recorder.Body.String())
	}
	return body
}

// assertBillingSanitised500 is the shared KEL-61 assertion for one endpoint:
// the unmapped-error branch answers 500 with a fixed generic message inside
// the unchanged envelope, never quotes the internal error, and the server log
// does carry the original error.
func assertBillingSanitised500(t *testing.T, recorder *httptest.ResponseRecorder, logBuffer *bytes.Buffer, wantMessage string) {
	t.Helper()
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", recorder.Code, recorder.Body.String())
	}
	body := decodeBillingFiveHundredBody(t, recorder)
	if body.Status != "error" {
		t.Fatalf("status field = %q, want error", body.Status)
	}
	if body.Data != nil {
		t.Fatalf("data = %v, want nil", body.Data)
	}
	if body.Message != wantMessage {
		t.Fatalf("message = %q, want the fixed generic message %q", body.Message, wantMessage)
	}
	if strings.Contains(recorder.Body.String(), billingSensitiveErrorMarker) {
		t.Fatal("500 body leaks the internal error marker")
	}
	if !strings.Contains(logBuffer.String(), billingSensitiveErrorMarker) {
		t.Fatalf("server log %q does not contain the original error", logBuffer.String())
	}
}

// TestCancelInternalEnrollmentPayment500KeepsInternalErrorOutOfResponse
// drives the default (unmapped) error branch of the internal cancel endpoint
// with an internal-looking database error. KEL-61: the body must not quote
// the error, and the original error must reach the server log instead.
func TestCancelInternalEnrollmentPayment500KeepsInternalErrorOutOfResponse(t *testing.T) {
	var logBuffer bytes.Buffer
	restore := billingCapturedSlogRecords(&logBuffer)
	defer restore()

	stub := &transactionUsecaseStub{err: errors.New(billingSensitiveErrorMarker)}
	recorder := callInternalCancel(t, stub, `{"enrollment_id":"`+uuid.NewString()+`"}`)

	assertBillingSanitised500(t, recorder, &logBuffer, "Failed to cancel transaction")
}

// TestGenerateSubscriptionPayment500KeepsInternalErrorOutOfResponse covers
// the internal generate endpoint: same marker, same fixed generic 500, the
// original error only in the log. The validation 400 above it intentionally
// keeps its binding detail and is covered by the unchanged existing tests.
func TestGenerateSubscriptionPayment500KeepsInternalErrorOutOfResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logBuffer bytes.Buffer
	restore := billingCapturedSlogRecords(&logBuffer)
	defer restore()

	stub := &generateErrTransactionStub{err: errors.New(billingSensitiveErrorMarker)}
	router := gin.New()
	router.POST("/internal/billing/transactions", NewTransactionHandler(stub, nil).GenerateInternalSubscriptionPayment)

	body := fmt.Sprintf(`{"tenant_id":%q,"student_id":%q,"class_id":%q,"enrollment_id":%q,"parent_id":%q,"billing_cycle":"monthly","subtotal_amount":150000}`,
		uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString())
	request := httptest.NewRequest(http.MethodPost, "/internal/billing/transactions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assertBillingSanitised500(t, recorder, &logBuffer, "Failed to generate subscription payment")
}
