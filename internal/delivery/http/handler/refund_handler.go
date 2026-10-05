package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"gorm.io/gorm"
)

type RefundHandler struct{ refunds repository.RefundRepository }

func NewRefundHandler(repo repository.RefundRepository) *RefundHandler {
	return &RefundHandler{refunds: repo}
}

// Record godoc
// @Summary Record a full manual refund
// @Description Tenant-only billing:refund. Records transfer evidence without changing wallet balances. Replays return the original record. Enrollment termination is retried asynchronously; completed enrollments remain unchanged.
// @Tags Billing
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Transaction UUID"
// @Param body body domain.RefundRequest true "Refund evidence"
// @Success 200 {object} domain.HTTPResponse{data=domain.TransactionRefund}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/transactions/{id}/refund [post]
func (h *RefundHandler) Record(c *gin.Context) {
	tenant, e1 := uuid.Parse(c.GetString("tenant_id"))
	actor, e2 := uuid.Parse(c.GetString("user_id"))
	if c.GetBool("is_parent") || e1 != nil || e2 != nil || tenant == uuid.Nil || actor == uuid.Nil {
		c.JSON(403, gin.H{"status": "error", "message": "Invalid tenant context"})
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	var req domain.RefundRequest
	if err != nil || c.ShouldBindJSON(&req) != nil {
		c.JSON(400, gin.H{"status": "error", "message": "Invalid refund request"})
		return
	}
	refund, err := h.refunds.RecordRefund(c.Request.Context(), tenant, actor, id, req)
	code, message := 500, "Failed to record refund"
	switch {
	case err == nil:
		c.JSON(200, gin.H{"status": "success", "message": "Refund recorded", "data": refund})
		return
	case errors.Is(err, repository.ErrInvalidRefund):
		code, message = 400, err.Error()
	case errors.Is(err, gorm.ErrRecordNotFound):
		code, message = 404, "Transaction not found"
	case errors.Is(err, domain.ErrInvalidTransactionStatus):
		code, message = 409, "Only paid transactions can be refunded"
	}
	c.JSON(code, gin.H{"status": "error", "message": message, "data": nil})
}
