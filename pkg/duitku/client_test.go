package duitku

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func TestValidateCallbackSignature(t *testing.T) {
	client := NewClient("https://example.test", "api-key", "D123", nil)
	signature := client.signature("D12310000order-1")

	tests := []struct {
		name    string
		payload domain.DuitkuCallbackPayload
		valid   bool
	}{
		{
			name: "valid signature",
			payload: domain.DuitkuCallbackPayload{
				MerchantCode: "D123", Amount: "10000", MerchantOrderID: "order-1", Signature: signature,
			},
			valid: true,
		},
		{
			name: "tampered amount",
			payload: domain.DuitkuCallbackPayload{
				MerchantCode: "D123", Amount: "90000", MerchantOrderID: "order-1", Signature: signature,
			},
			valid: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := client.ValidateCallbackSignature(&test.payload); got != test.valid {
				t.Fatalf("ValidateCallbackSignature() = %v, want %v", got, test.valid)
			}
		})
	}
}

func TestCreateInvoice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/inquiry" {
			t.Fatalf("path = %q, want /v2/inquiry", request.URL.Path)
		}
		var payload inquiryRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.MerchantOrderID != "order-1" || payload.PaymentAmount != 10000 || payload.Signature == "" {
			t.Fatalf("unexpected inquiry payload: %+v", payload)
		}
		_, _ = writer.Write([]byte(`{"reference":"D-REF","paymentUrl":"https://pay.test/D-REF","statusCode":"00","statusMessage":"SUCCESS"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "api-key", "D123", server.Client())
	result, err := client.CreateInvoice(context.Background(), &domain.CreateInvoiceRequest{
		MerchantOrderID: "order-1", Amount: 10000, ProductDetails: "Subscription", Email: "user@example.com",
		PaymentMethod: "VC", CallbackURL: "https://merchant.test/callback", ReturnURL: "https://merchant.test/return",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reference != "D-REF" || result.PaymentURL == "" {
		t.Fatalf("unexpected result: %+v", result)
	}

	mac := hmac.New(sha256.New, []byte("api-key"))
	_, _ = mac.Write([]byte("D123order-110000"))
	if client.requestSignature("order-1", 10000) != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("request signature does not match Duitku HMAC-SHA256 formula")
	}
}
