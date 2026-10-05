package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

func VoucherPreview(service *usecase.VoucherPreviewUsecase) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req usecase.VoucherPreviewRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"status": "error", "message": "Invalid voucher preview request", "data": nil})
			return
		}
		result, err := service.Preview(c.Request.Context(), req)
		if errors.Is(err, domain.ErrVoucherRejected) {
			c.JSON(422, gin.H{"status": "error", "code": domain.VoucherRejectedCode, "message": domain.VoucherRejectedMessage, "data": nil})
			return
		}
		if err != nil {
			c.JSON(500, gin.H{"status": "error", "message": "Failed to preview voucher", "data": nil})
			return
		}
		c.JSON(200, gin.H{"status": "success", "data": result})
	}
}
