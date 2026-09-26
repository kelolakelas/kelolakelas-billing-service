package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// transactionUsecaseStub records what the internal cancellation handler asked for and
// returns a canned outcome. The handler's whole job is the request/response contract, so
// the stub is deliberately small: it proves the handler forwards the enrollment id and
// maps each documented failure onto its status code.
type transactionUsecaseStub struct {
	response       *domain.TransactionResponse
	err            error
	calls          int
	lastEnrollment uuid.UUID
	// lastQuery records the filter the list handler forwarded, so the tests can prove a
	// status the API accepts is the status the domain search actually receives.
	lastQuery domain.TransactionQuery
	// lastSenderEmail records the sender_email the generate handler forwarded, so the
	// tests can prove binding validation and forwarding in one place.
	lastSenderEmail string
}

func (s *transactionUsecaseStub) CreateTransaction(context.Context, *domain.Transaction) error {
	return nil
}

func (s *transactionUsecaseStub) GetTransaction(context.Context, uuid.UUID) (*domain.Transaction, error) {
	return nil, nil
}

func (s *transactionUsecaseStub) GenerateSubscriptionPayment(_ context.Context, request *domain.GenerateSubscriptionPaymentRequest) (*domain.GenerateSubscriptionPaymentResponse, error) {
	s.calls++
	s.lastSenderEmail = request.SenderEmail
	return &domain.GenerateSubscriptionPaymentResponse{TransactionID: uuid.New(), Status: domain.TransactionStatusPending}, nil
}

func (s *transactionUsecaseStub) CancelEnrollmentPayment(_ context.Context, enrollmentID uuid.UUID) (*domain.TransactionResponse, error) {
	s.calls++
	s.lastEnrollment = enrollmentID
	return s.response, s.err
}

func (s *transactionUsecaseStub) HandleDuitkuWebhook(context.Context, *domain.DuitkuCallbackPayload) error {
	return nil
}

func (s *transactionUsecaseStub) List(_ context.Context, _, _ *uuid.UUID, query domain.TransactionQuery) (*domain.TransactionListResponse, error) {
	s.lastQuery = query
	return &domain.TransactionListResponse{}, nil
}

func (s *transactionUsecaseStub) GetByIDScoped(context.Context, *uuid.UUID, *uuid.UUID, uuid.UUID) (*domain.TransactionResponse, error) {
	return nil, nil
}

func callInternalCancel(t *testing.T, stub *transactionUsecaseStub, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/internal/billing/transactions/cancel", NewTransactionHandler(stub, nil).CancelInternalEnrollmentPayment)

	request := httptest.NewRequest(http.MethodPost, "/internal/billing/transactions/cancel", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestCancelInternalEnrollmentPaymentReturnsCancelledTransaction(t *testing.T) {
	enrollmentID := uuid.New()
	stub := &transactionUsecaseStub{response: &domain.TransactionResponse{
		ID: uuid.New(), EnrollmentID: enrollmentID, Status: domain.TransactionStatusCancelled,
	}}
	recorder := callInternalCancel(t, stub, `{"enrollment_id":"`+enrollmentID.String()+`"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if stub.calls != 1 || stub.lastEnrollment != enrollmentID {
		t.Fatalf("calls=%d enrollment=%s, want the requested enrollment forwarded", stub.calls, stub.lastEnrollment)
	}
	var payload struct {
		Status string                     `json:"status"`
		Data   domain.TransactionResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.Status != domain.TransactionStatusCancelled {
		t.Fatalf("payload status=%q, want %q", payload.Data.Status, domain.TransactionStatusCancelled)
	}
}

// Academic decides whether a 404 is a real error, so the handler must keep the sentinel
// distinguishable from a conflict instead of collapsing both into one code.
func TestCancelInternalEnrollmentPaymentMapsUnknownEnrollmentToNotFound(t *testing.T) {
	stub := &transactionUsecaseStub{err: domain.ErrTransactionNotFound}
	recorder := callInternalCancel(t, stub, `{"enrollment_id":"`+uuid.NewString()+`"}`)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", recorder.Code, recorder.Body.String())
	}
}

func TestCancelInternalEnrollmentPaymentMapsSettledTransactionToConflict(t *testing.T) {
	stub := &transactionUsecaseStub{err: domain.ErrInvalidTransactionStatus}
	recorder := callInternalCancel(t, stub, `{"enrollment_id":"`+uuid.NewString()+`"}`)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", recorder.Code, recorder.Body.String())
	}
}

func TestCancelInternalEnrollmentPaymentRejectsMalformedBody(t *testing.T) {
	stub := &transactionUsecaseStub{}
	recorder := callInternalCancel(t, stub, `{"enrollment_id":"not-a-uuid"}`)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	if stub.calls != 0 {
		t.Fatalf("calls=%d, want the invalid request to be rejected before the usecase", stub.calls)
	}
}

func TestCancelInternalEnrollmentPaymentReportsUnexpectedFailure(t *testing.T) {
	stub := &transactionUsecaseStub{err: errors.New("database is unavailable")}
	recorder := callInternalCancel(t, stub, `{"enrollment_id":"`+uuid.NewString()+`"}`)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want 500", recorder.Code, recorder.Body.String())
	}
}
