package handler

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/duitku"
)

func TestInvalidWebhookSignatureIsCorrelatedWithoutSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	r := gin.New()
	r.Use(middleware.RequestLog())
	r.POST("/webhooks/duitku", NewTransactionHandler(nil, duitku.NewClient("https://example.test", "test-key", "test-merchant", nil)).HandleDuitkuWebhook)
	body := `{"merchantCode":"test-merchant","amount":"10000","merchantOrderId":"private-order","resultCode":"00","signature":"private-signature"}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/duitku", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer private-token")
	req.Header.Set("X-Request-ID", "gateway-trace")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || w.Header().Get("X-Request-ID") != "gateway-trace" {
		t.Fatalf("response = %d %q", w.Code, w.Header().Get("X-Request-ID"))
	}
	if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), `"request_id":"gateway-trace"`) {
		t.Fatalf("missing correlated warning: %s", logs.String())
	}
	for _, secret := range []string{"private-token", "private-order", "private-signature", "test-key"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("sensitive value logged: %s", secret)
		}
	}
}
