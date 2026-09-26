package main

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const probeTimeout = time.Second

func readinessHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), probeTimeout)
		defer cancel()
		status, component, code := "healthy", "healthy", http.StatusOK
		if db == nil || db.PingContext(ctx) != nil {
			status, component, code = "unavailable", "unavailable", http.StatusServiceUnavailable
		}
		c.JSON(code, gin.H{"status": status, "service": "billing-service", "components": gin.H{"database": component}})
	}
}
