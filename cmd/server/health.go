package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func healthHandler(service string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "healthy",
			"service": service,
		})
	}
}
