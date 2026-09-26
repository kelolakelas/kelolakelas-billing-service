package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// KEL-75: sender_email is optional, but a non-empty value must be a valid email
// address. The binding rejects anything else before the use case runs, so no
// transaction or subscription row is ever created for it.
func callInternalGenerate(t *testing.T, stub *transactionUsecaseStub, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/internal/billing/transactions", NewTransactionHandler(stub, nil).GenerateInternalSubscriptionPayment)

	request := httptest.NewRequest(http.MethodPost, "/internal/billing/transactions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func generatePaymentBody(senderEmail string) string {
	return `{"tenant_id":"11111111-1111-1111-1111-111111111111","student_id":"22222222-2222-2222-2222-222222222222","class_id":"33333333-3333-3333-3333-333333333333","enrollment_id":"44444444-4444-4444-4444-444444444444","parent_id":"55555555-5555-5555-5555-555555555555","billing_cycle":"monthly","subtotal_amount":40000,"sender_email":"` + senderEmail + `"}`
}

func TestGenerateInternalSubscriptionPaymentAcceptsValidSenderEmail(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callInternalGenerate(t, stub, generatePaymentBody("parent@example.com"))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s, want 201", recorder.Code, recorder.Body.String())
	}
	if stub.calls != 1 {
		t.Fatalf("usecase calls=%d, want 1", stub.calls)
	}
	if stub.lastSenderEmail != "parent@example.com" {
		t.Fatalf("sender_email=%q, want it forwarded to the usecase", stub.lastSenderEmail)
	}
}

func TestGenerateInternalSubscriptionPaymentAcceptsEmptySenderEmail(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callInternalGenerate(t, stub, generatePaymentBody(""))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s, want 201 (rollout compatibility)", recorder.Code, recorder.Body.String())
	}
	if stub.calls != 1 {
		t.Fatalf("usecase calls=%d, want 1", stub.calls)
	}
	if stub.lastSenderEmail != "" {
		t.Fatalf("sender_email=%q, want it forwarded unchanged", stub.lastSenderEmail)
	}
}

func TestGenerateInternalSubscriptionPaymentRejectsInvalidSenderEmail(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callInternalGenerate(t, stub, generatePaymentBody("bukan-email"))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	if stub.calls != 0 {
		t.Fatalf("usecase calls=%d, want the invalid request rejected before any transaction is created", stub.calls)
	}
}
