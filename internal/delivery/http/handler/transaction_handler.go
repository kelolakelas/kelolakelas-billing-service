package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

type TransactionHandler struct {
	txUsecase usecase.TransactionUsecase
}

func NewTransactionHandler(txUsecase usecase.TransactionUsecase) *TransactionHandler {
	return &TransactionHandler{
		txUsecase: txUsecase,
	}
}

// GenerateSubscriptionPayment godoc
// @Summary Generate class subscription payment link
// @Description Initiates class subscription payment via Flip Accept Payment
// @Tags Billing
// @Accept json
// @Produce json
// @Param request body domain.GenerateSubscriptionPaymentRequest true "Payment request details"
// @Success 201 {object} domain.HTTPResponse{data=domain.GenerateSubscriptionPaymentResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Router /api/v1/billing/transactions [post]
func (h *TransactionHandler) GenerateSubscriptionPayment(c *gin.Context) {
	var req domain.GenerateSubscriptionPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "Invalid request payload: " + err.Error(),
			"data":    nil,
		})
		return
	}

	resp, err := h.txUsecase.GenerateSubscriptionPayment(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"status":  "success",
		"message": "Subscription payment generated successfully",
		"data":    resp,
	})
}

// HandleFlipWebhook godoc
// @Summary Handle Flip payment webhook callback
// @Description Callback endpoint for Flip Accept Payment status updates
// @Tags Billing
// @Accept json,x-www-form-urlencoded
// @Produce json
// @Param x-flip-validation-token header string false "Flip validation token"
// @Param payload body domain.FlipWebhookPayload true "Flip webhook payload"
// @Success 200 {object} domain.HTTPResponse
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Router /api/v1/billing/webhooks/flip [post]
func (h *TransactionHandler) HandleFlipWebhook(c *gin.Context) {
	validationToken := c.GetHeader("x-flip-validation-token")
	if validationToken == "" {
		validationToken = c.GetHeader("X-Flip-Validation-Token")
	}

	var payload domain.FlipWebhookPayload
	if err := c.ShouldBind(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "Failed to bind webhook payload: " + err.Error(),
		})
		return
	}

	if err := h.txUsecase.HandleFlipWebhook(c.Request.Context(), &payload, validationToken); err != nil {
		if errors.Is(err, domain.ErrInvalidWebhookToken) {
			c.JSON(http.StatusUnauthorized, gin.H{
				"status":  "error",
				"message": "Invalid validation token",
			})
			return
		}
		if errors.Is(err, domain.ErrTransactionNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"status":  "error",
				"message": "Transaction not found",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "Failed to process webhook: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Webhook processed successfully",
	})
}
