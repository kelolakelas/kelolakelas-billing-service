package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestInternalServiceAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		credential string
		header     string
		status     int
		called     bool
	}{
		{name: "valid", credential: "shared-secret", header: "shared-secret", status: http.StatusNoContent, called: true},
		{name: "configured credential empty", credential: "", header: "", status: http.StatusUnauthorized},
		{name: "header missing", credential: "shared-secret", status: http.StatusUnauthorized},
		{name: "wrong credential", credential: "shared-secret", header: "wrong-secret", status: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			called := false
			router.Use(InternalServiceAuth(test.credential))
			router.GET("/internal", func(c *gin.Context) {
				called = true
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/internal", nil)
			if test.header != "" {
				req.Header.Set("X-Internal-Service-Credential", test.header)
			}
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != test.status || called != test.called {
				t.Fatalf("status=%d called=%t", resp.Code, called)
			}
			if resp.Code == http.StatusUnauthorized && resp.Body.String() != `{"data":null,"message":"Invalid internal service credential","status":"error"}` {
				t.Fatalf("unexpected response: %s", resp.Body.String())
			}
		})
	}
}
