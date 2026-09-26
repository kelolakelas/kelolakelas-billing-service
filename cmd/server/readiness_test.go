package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
)

func TestReadinessAndLiveness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	router := gin.New()
	router.GET("/health", healthHandler("billing-service"))
	router.GET("/ready", readinessHandler(db))
	for _, tc := range []struct {
		name      string
		pingError bool
		path      string
		code      int
		component string
	}{
		{"ready", false, "/ready", 200, "healthy"},
		{"database down", true, "/ready", 503, "unavailable"},
		{"liveness unaffected", true, "/health", 200, "healthy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.path == "/ready" {
				if tc.pingError {
					mock.ExpectPing().WillReturnError(http.ErrServerClosed)
				} else {
					mock.ExpectPing()
				}
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.component) {
				t.Fatalf("response: %d %s", w.Code, w.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
