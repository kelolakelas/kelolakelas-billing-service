package duitku

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func TestInquiryPaymentInstructionsAndRedactedErrors(t *testing.T) {
	for _, test := range []struct {
		name, body  string
		code        int
		va, qr, app string
		wantError   bool
	}{
		{"va", `{"reference":"R1","paymentUrl":"https://pay.test/1","vaNumber":"7007014001444348","statusCode":"00"}`, 200, "7007014001444348", "", "", false},
		{"qr", `{"reference":"R2","paymentUrl":"https://pay.test/2","qrString":"000201010212","appUrl":"https://app.test/pay","statusCode":"00"}`, 200, "", "000201010212", "https://app.test/pay", false},
		{"hosted only", `{"reference":"R3","paymentUrl":"https://pay.test/3","statusCode":"00"}`, 200, "", "", "", false},
		{"provider failure", `{"statusMessage":"sensitive-marker","vaNumber":"sensitive-marker","statusCode":"01"}`, 200, "", "", "", true},
		{"HTTP failure", `{"apiKey":"sensitive-marker","vaNumber":"sensitive-marker"}`, 502, "", "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.code)
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			invoice, err := NewClient(server.URL, "api-key", "D123", server.Client()).CreateInvoice(context.Background(), &domain.CreateInvoiceRequest{MerchantOrderID: "order", Amount: 10000})
			if test.wantError {
				if err == nil || strings.Contains(err.Error(), "sensitive-marker") || invoice != nil {
					t.Fatalf("provider error leaked response: invoice=%v err=%v", invoice, err)
				}
				return
			}
			if err != nil || invoice == nil || invoice.VANumber != test.va || invoice.QRString != test.qr || invoice.AppURL != test.app || invoice.PaymentURL == "" {
				t.Fatalf("parsed invoice=%+v err=%v", invoice, err)
			}
		})
	}
}
