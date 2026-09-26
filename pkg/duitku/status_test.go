package duitku

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTransactionStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/transactionStatus" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			MerchantCode    string `json:"merchantCode"`
			MerchantOrderID string `json:"merchantOrderId"`
			Signature       string `json:"signature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.MerchantCode != "D123" || payload.MerchantOrderID != "order-1" || payload.Signature != NewClient("", "secret", "D123", nil).signature("D123order-1") {
			t.Errorf("invalid status request: %+v", payload)
		}
		_, _ = w.Write([]byte(`{"merchantOrderId":"order-1","reference":"REF","amount":"40000","statusCode":"00"}`))
	}))
	defer server.Close()
	status, err := NewClient(server.URL, "secret", "D123", server.Client()).TransactionStatus(context.Background(), "order-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.MerchantOrderID != "order-1" || status.Amount != 40000 || status.StatusCode != "00" {
		t.Fatalf("status = %+v", status)
	}
}

func TestTransactionStatusRejectsUnusableResponse(t *testing.T) {
	for _, response := range []string{`{"statusCode":"00","amount":"40000"}`, `{"merchantOrderId":"order-1","statusCode":"00","amount":"bad"}`, `{`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			if _, err := NewClient(server.URL, "secret", "D123", server.Client()).TransactionStatus(context.Background(), "order-1"); err == nil {
				t.Fatal("malformed status accepted")
			}
		})
	}
}
