package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/metadata"
)

func TestRequestLogCorrelationAndRedaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	r := gin.New()
	r.Use(RequestLog())
	r.GET("/private/:id", func(c *gin.Context) {
		if RequestID(c.Request.Context()) != "trace-1" {
			t.Error("missing request context ID")
		}
		md, _ := metadata.FromOutgoingContext(OutgoingContext(c.Request.Context()))
		if md.Get("x-request-id")[0] != "trace-1" {
			t.Error("missing gRPC request ID")
		}
		c.Status(http.StatusForbidden)
	})
	req := httptest.NewRequest(http.MethodGet, "/private/secret-path?password=hidden-query", nil)
	req.Header.Set("X-Request-ID", "trace-1")
	req.Header.Set("Authorization", "Bearer hidden-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || w.Header().Get("X-Request-ID") != "trace-1" {
		t.Fatalf("response = %d %q", w.Code, w.Header().Get("X-Request-ID"))
	}
	for _, fragment := range []string{"trace-1", `"status":403`, `"route":"/private/:id"`} {
		if !strings.Contains(logs.String(), fragment) {
			t.Errorf("missing %s in access log", fragment)
		}
	}
	for _, secret := range []string{"hidden-token", "hidden-query", "secret-path"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("sensitive value logged: %s", secret)
		}
	}
}

func TestRequestLogGeneratesSafeID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestLog())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	for _, input := range []string{"", "bad\nheader", strings.Repeat("x", 65)} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if input != "" {
			req.Header.Set("X-Request-ID", input)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if id := w.Header().Get("X-Request-ID"); !validRequestID(id) || id == input {
			t.Errorf("unsafe generated ID %q for %q", id, input)
		}
	}
}
