package academic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/requestid"
)

func TestEnrollmentCallsForwardHTTPContextIDOnSuccessAndFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"activate", http.StatusNoContent},
		{"release", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const credential = "private-credential"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Request-ID") != "gateway-trace" || r.Header.Get("X-Internal-Service-Credential") != credential || r.Header.Get("Authorization") != "" {
					t.Errorf("correlation or credential missing from internal call")
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			client := NewClient(server.URL, credential)
			r := gin.New()
			r.Use(middleware.RequestLog())
			r.GET("/enrollment", func(c *gin.Context) {
				var err error
				if tc.name == "activate" {
					err = client.ActivateEnrollment(c.Request.Context(), uuid.New())
				} else {
					err = client.ReleaseEnrollment(c.Request.Context(), uuid.New())
				}
				if tc.status >= 400 && (err == nil || !strings.Contains(err.Error(), "status code 503") || strings.Contains(err.Error(), credential)) {
					t.Errorf("unexpected safe failure: %v", err)
				} else if tc.status < 400 && err != nil {
					t.Errorf("unexpected success error: %v", err)
				}
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/enrollment", nil)
			req.Header.Set("X-Request-ID", "gateway-trace")
			req.Header.Set("Authorization", "Bearer do-not-forward")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("response status = %d", w.Code)
			}
		})
	}
}

func TestEnrollmentCallsWithoutHTTPIDDoNotInventHeader(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"worker", context.Background()},
		{"invalid context ID", requestid.WithContext(context.Background(), "bad\nvalue")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("X-Request-ID"); got != "" {
					t.Errorf("unexpected request ID: %q", got)
				}
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer server.Close()
			err := NewClient(server.URL, "credential").ReleaseEnrollment(tc.ctx, uuid.New())
			if err == nil || !strings.Contains(err.Error(), "status code 502") {
				t.Errorf("failure not preserved: %v", err)
			}
		})
	}
}
