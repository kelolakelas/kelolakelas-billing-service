package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func callGenerateWithError(t *testing.T, err error) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/internal/billing/transactions", NewTransactionHandler(&generateErrTransactionStub{err: err}, nil).GenerateInternalSubscriptionPayment)
	body := fmt.Sprintf(`{"tenant_id":%q,"student_id":%q,"class_id":%q,"enrollment_id":%q,"parent_id":%q,"billing_cycle":"monthly","subtotal_amount":2000,"payment_gateway_fee":1000}`,
		uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString())
	request := httptest.NewRequest(http.MethodPost, "/internal/billing/transactions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// KEL-99: a fee above the gross amount answers 422 with the stable code and
// the Indonesian message.
func TestGenerateSubscriptionPaymentFeeAboveGrossIs422WithStableCode(t *testing.T) {
	recorder := callGenerateWithError(t, fmt.Errorf("wrapped: %w", domain.ErrPlatformFeeExceedsGross))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "platform_fee_exceeds_gross" || body["message"] != "Biaya platform melebihi jumlah pembayaran" || body["status"] != "error" {
		t.Fatalf("body = %v", body)
	}
}

// KEL-99: an unavailable policy is a 503 that does not leak the cause.
func TestGenerateSubscriptionPaymentPolicyUnavailableIs503(t *testing.T) {
	var logBuffer bytes.Buffer
	restore := billingCapturedSlogRecords(&logBuffer)
	defer restore()

	recorder := callGenerateWithError(t, fmt.Errorf("%w: rpc error: dial tcp 10.0.0.7:50051", domain.ErrPlatformFeePolicyUnavailable))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "10.0.0.7") {
		t.Fatal("503 body leaks the internal cause")
	}
	if !strings.Contains(recorder.Body.String(), "platform_fee_policy_unavailable") {
		t.Fatalf("body = %s, want code platform_fee_policy_unavailable", recorder.Body.String())
	}
	if !strings.Contains(logBuffer.String(), "10.0.0.7") {
		t.Fatal("server log does not carry the original error")
	}
}
