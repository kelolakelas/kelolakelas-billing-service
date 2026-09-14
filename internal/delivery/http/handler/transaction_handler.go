package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

type TransactionHandler struct {
	txUsecase      usecase.TransactionUsecase
	paymentGateway domain.PaymentGateway
}

// List godoc
// @Summary List billing transactions
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Success 200 {object} domain.HTTPResponse{data=domain.TransactionListResponse}
// @Router /api/v1/billing/transactions [get]
func (h *TransactionHandler) List(c *gin.Context) {
	var tenantID uuid.UUID
	var err error
	if !c.GetBool("is_parent") {
		tenantID, err = uuid.Parse(c.GetString("tenant_id"))
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid tenant context", "data": nil})
			return
		}
	}
	q := domain.TransactionQuery{Page: 1, PageSize: 20, Status: c.Query("status"), Search: c.Query("search")}
	if q.Status != "" {
		switch q.Status {
		case "pending", "paid", "failed", "expired", "cancelled", "refunded":
		default:
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid transaction status", "data": nil})
			return
		}
	}
	for key, target := range map[string]*int{"page": &q.Page, "page_size": &q.PageSize} {
		if v := c.Query(key); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 || (key == "page_size" && n > 100) {
				c.JSON(400, gin.H{"status": "error", "message": "Invalid pagination", "data": nil})
				return
			}
			*target = n
		}
	}
	for key, target := range map[string]**uuid.UUID{"student_id": &q.StudentID, "enrollment_id": &q.EnrollmentID} {
		if v := c.Query(key); v != "" {
			id, e := uuid.Parse(v)
			if e != nil {
				c.JSON(400, gin.H{"status": "error", "message": "Invalid " + key, "data": nil})
				return
			}
			*target = &id
		}
	}
	for key, target := range map[string]**time.Time{"date_from": &q.DateFrom, "date_to": &q.DateTo} {
		if v := c.Query(key); v != "" {
			d, e := time.Parse("2006-01-02", v)
			if e != nil {
				c.JSON(400, gin.H{"status": "error", "message": "Invalid " + key, "data": nil})
				return
			}
			*target = &d
		}
	}
	var tenantScope, parentScope *uuid.UUID
	if c.GetBool("is_parent") {
		parentID, parseErr := uuid.Parse(c.GetString("user_id"))
		if parseErr != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid parent context", "data": nil})
			return
		}
		parentScope = &parentID
	} else {
		tenantScope = &tenantID
	}
	result, err := h.txUsecase.List(c.Request.Context(), tenantScope, parentScope, q)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to fetch transactions", "data": nil})
		return
	}
	c.JSON(200, gin.H{"status": "success", "message": "Transactions fetched successfully", "data": result})
}

// Get godoc
// @Summary Get billing transaction
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param id path string true "Transaction UUID"
// @Success 200 {object} domain.HTTPResponse{data=domain.TransactionResponse}
// @Router /api/v1/billing/transactions/{id} [get]
func (h *TransactionHandler) Get(c *gin.Context) {
	var tenantID uuid.UUID
	var err error
	if !c.GetBool("is_parent") {
		tenantID, err = uuid.Parse(c.GetString("tenant_id"))
		if err != nil {
			c.JSON(401, gin.H{"status": "error", "message": "Invalid tenant context", "data": nil})
			return
		}
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"status": "error", "message": "Invalid transaction ID", "data": nil})
		return
	}
	var tenantScope, parentScope *uuid.UUID
	if c.GetBool("is_parent") {
		parentID, parseErr := uuid.Parse(c.GetString("user_id"))
		if parseErr != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid parent context", "data": nil})
			return
		}
		parentScope = &parentID
	} else {
		tenantScope = &tenantID
	}
	result, err := h.txUsecase.GetByIDScoped(c.Request.Context(), tenantScope, parentScope, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"status": "error", "message": "Transaction not found", "data": nil})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to fetch transaction", "data": nil})
		return
	}
	c.JSON(200, gin.H{"status": "success", "message": "Transaction fetched successfully", "data": result})
}

func NewTransactionHandler(txUsecase usecase.TransactionUsecase, paymentGateway domain.PaymentGateway) *TransactionHandler {
	return &TransactionHandler{
		txUsecase: txUsecase, paymentGateway: paymentGateway,
	}
}

// GenerateInternalSubscriptionPayment godoc
// @Summary Generate subscription payment from an internal service
// @Description Internal service-to-service endpoint for generating a billing invoice.
// @Tags Billing
// @Accept json
// @Produce json
// @Param request body domain.GenerateSubscriptionPaymentRequest true "Payment request details"
// @Success 201 {object} domain.HTTPResponse{data=domain.GenerateSubscriptionPaymentResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Router /internal/billing/transactions [post]
func (h *TransactionHandler) GenerateInternalSubscriptionPayment(c *gin.Context) {
	h.generateSubscriptionPayment(c)
}

func (h *TransactionHandler) generateSubscriptionPayment(c *gin.Context) {
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

// HandleDuitkuWebhook godoc
// @Summary Handle Duitku payment webhook callback
// @Description Callback endpoint for Duitku payment status updates
// @Tags Billing
// @Accept json,x-www-form-urlencoded
// @Produce json
// @Param payload body domain.DuitkuCallbackPayload true "Duitku callback payload"
// @Success 200 {object} domain.HTTPResponse
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Router /api/v1/billing/webhooks/duitku [post]
func (h *TransactionHandler) HandleDuitkuWebhook(c *gin.Context) {
	var payload domain.DuitkuCallbackPayload
	if err := c.ShouldBind(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "Invalid webhook payload",
		})
		return
	}

	if !h.paymentGateway.ValidateCallbackSignature(&payload) {
		c.JSON(http.StatusUnauthorized, gin.H{
			"status":  "error",
			"message": "Invalid Duitku callback signature",
		})
		return
	}

	if err := h.txUsecase.HandleDuitkuWebhook(c.Request.Context(), &payload); err != nil {
		if errors.Is(err, domain.ErrInvalidWebhookSignature) {
			c.JSON(http.StatusUnauthorized, gin.H{
				"status":  "error",
				"message": "Invalid callback signature",
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
			"message": "Failed to process webhook",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Webhook processed successfully",
	})
}
