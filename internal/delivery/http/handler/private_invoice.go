package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"gorm.io/gorm"
	"net/http"
)

// ReadPrivateRequestInvoice is mounted only behind InternalServiceAuth; it never
// changes payment state or returns the full financial transaction record.
// @Summary Read one scoped initial private invoice
// @Tags Internal billing
// @Accept json
// @Produce json
// @Security InternalServiceCredential
// @Param scope body domain.PrivateInvoiceScope true "Exact persisted private purchase scope"
// @Success 200 {object} domain.HTTPResponse{data=domain.PrivateInvoice}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /internal/billing/private-request-invoice [post]
func ReadPrivateRequestInvoice(reader domain.PrivateInvoiceReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		var scope domain.PrivateInvoiceScope
		if c.ShouldBindJSON(&scope) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Complete private request invoice scope required"})
			return
		}
		result, err := reader.ReadPrivateInvoice(c.Request.Context(), scope)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "Private invoice not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Invoice unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "data": result})
	}
}
