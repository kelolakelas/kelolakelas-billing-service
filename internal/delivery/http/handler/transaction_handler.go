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
// @Description Parents see their own transactions. Tenant members need the `billing:read` permission in their tenant: without it the answer is 403, and while identity cannot be asked it is 503.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Success 200 {object} domain.HTTPResponse{data=domain.TransactionListResponse}
// @Failure 403 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
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
	if q.Status != "" && !domain.IsTransactionStatusFilterValue(q.Status) {
		// The accepted values come from the domain instead of a local list, so every
		// status the code can actually write stays filterable and cannot drift.
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid transaction status", "data": nil})
		return
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
// @Description Same authorization as the list: parents read their own transaction, tenant members need `billing:read` (403 without it, 503 while identity cannot be asked).
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param id path string true "Transaction UUID"
// @Success 200 {object} domain.HTTPResponse{data=domain.TransactionResponse}
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
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

// CancelInternalEnrollmentPayment godoc
// @Summary Cancel the unpaid transaction of a cancelled enrollment
// @Description Internal service-to-service endpoint that marks the unpaid transaction of an enrollment as `cancelled`. The transition is idempotent, never rewrites a paid or refunded transaction, and reports 404 when the enrollment has no transaction at all.
// @Tags Billing
// @Accept json
// @Produce json
// @Param request body domain.CancelEnrollmentPaymentRequest true "Enrollment to cancel"
// @Success 200 {object} domain.HTTPResponse{data=domain.TransactionResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Router /internal/billing/transactions/cancel [post]
func (h *TransactionHandler) CancelInternalEnrollmentPayment(c *gin.Context) {
	var req domain.CancelEnrollmentPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid cancellation request", "data": nil})
		return
	}
	result, err := h.txUsecase.CancelEnrollmentPayment(c.Request.Context(), req.EnrollmentID)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrTransactionNotFound):
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "Transaction not found", "data": nil})
		case errors.Is(err, domain.ErrInvalidTransactionStatus):
			c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "Transaction can no longer be cancelled", "data": nil})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error(), "data": nil})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Transaction cancelled successfully", "data": result})
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
