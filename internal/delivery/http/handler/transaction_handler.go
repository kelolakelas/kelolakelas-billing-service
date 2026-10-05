package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

type TransactionHandler struct {
	txUsecase      usecase.TransactionUsecase
	paymentGateway domain.PaymentGateway
}

// List godoc
// @Summary List billing transactions
// @Description Parents see their own transactions. Tenant members need the `billing:read` permission in their tenant: without it the answer is 403, and while identity cannot be asked it is 503. The date_from/date_to range filters on created_at by default; pass date_by=paid_at to filter on the payment date with inclusive UTC-day semantics matching the sales summary (ADR 0039).
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param date_by query string false "Date basis: created_at (default) or paid_at"
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
	q := domain.TransactionQuery{Page: 1, PageSize: 20, Status: c.Query("status"), Search: c.Query("search"), DateBy: c.Query("date_by")}
	if q.Status != "" && !domain.IsTransactionStatusFilterValue(q.Status) {
		// The accepted values come from the domain instead of a local list, so every
		// status the code can actually write stays filterable and cannot drift.
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid transaction status", "data": nil})
		return
	}
	if !domain.IsTransactionDateByValue(q.DateBy) {
		// An unknown basis must be rejected: silently falling back to
		// `created_at` would return rows the caller did not ask for while
		// looking like an empty paid-date range.
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid date basis", "data": nil})
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

// SalesSummary godoc
// @Summary Summarize paid tenant sales by currency and UTC paid date
// @Description Tenant members with billing:read only; parents are forbidden. Dates are inclusive YYYY-MM-DD UTC, at most 366 calendar days, defaulting to today and the preceding 29 days.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param from query string false "First UTC paid date"
// @Param to query string false "Last UTC paid date"
// @Success 200 {object} domain.HTTPResponse{data=domain.SalesSummaryResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/transactions/summary [get]
func (h *TransactionHandler) SalesSummary(c *gin.Context) {
	if c.GetBool("is_parent") {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "message": "Permission denied", "data": nil})
		return
	}
	tenantID, err := uuid.Parse(c.GetString("tenant_id"))
	if err != nil || tenantID == uuid.Nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid tenant context", "data": nil})
		return
	}
	// Date-only parameters use UTC calendar days, independent of server timezone.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from, to := today.AddDate(0, 0, -29), today
	if raw, present := c.GetQuery("from"); present {
		from, err = time.Parse("2006-01-02", raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid sales summary range", "data": nil})
			return
		}
	}
	if raw, present := c.GetQuery("to"); present {
		to, err = time.Parse("2006-01-02", raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid sales summary range", "data": nil})
			return
		}
	}
	if from.After(to) || to.AddDate(0, 0, 1).Sub(from) > 366*24*time.Hour {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid sales summary range", "data": nil})
		return
	}
	totals, err := h.txUsecase.SalesSummary(c.Request.Context(), tenantID, from, to.AddDate(0, 0, 1))
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "sales summary failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to fetch sales summary", "data": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Sales summary fetched successfully", "data": domain.SalesSummaryResponse{
		From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), Totals: totals,
	}})
}

// Export godoc
// @Summary Export tenant transactions as CSV
// @Description Tenant members with billing:read only; parents are forbidden. Accepts the same filters as the list (status, student_id, enrollment_id, search, date_from, date_to, date_by) but defaults to paid transactions on the paid date over the last 30 UTC days, so the default export reconciles with the sales summary. Days are inclusive YYYY-MM-DD UTC (ADR 0039: a UTC day ends at 07:00 WIB), at most 366 calendar days. Rows stream in batches, so a full-range export never loads fully into memory. Cells starting with `=`, `+`, `-`, or `@` carry a leading single quote so spreadsheets render them as text.
// @Tags Billing
// @Produce text/csv
// @Security BearerAuth
// @Param status query string false "Transaction status, defaults to paid"
// @Param student_id query string false "Student UUID"
// @Param enrollment_id query string false "Enrollment UUID"
// @Param search query string false "Merchant order or payment intent substring"
// @Param date_from query string false "First UTC date (YYYY-MM-DD)"
// @Param date_to query string false "Last UTC date (YYYY-MM-DD)"
// @Param date_by query string false "Date basis: created_at or paid_at, defaults to paid_at"
// @Success 200 {string} string "CSV bytes"
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/transactions/export [get]
func (h *TransactionHandler) Export(c *gin.Context) {
	if c.GetBool("is_parent") {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "message": "Permission denied", "data": nil})
		return
	}
	tenantID, err := uuid.Parse(c.GetString("tenant_id"))
	if err != nil || tenantID == uuid.Nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid tenant context", "data": nil})
		return
	}
	q := domain.TransactionQuery{Status: c.Query("status"), Search: c.Query("search"), DateBy: c.Query("date_by")}
	if q.Status == "" {
		// The export exists to reconcile bookkeeping with the sales summary,
		// which only ever counts paid transactions.
		q.Status = domain.TransactionStatusPaid
	}
	if q.Status != "" && !domain.IsTransactionStatusFilterValue(q.Status) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid transaction status", "data": nil})
		return
	}
	if q.DateBy == "" {
		// Same reason: the default export must match the summary's paid-date days.
		q.DateBy = domain.TransactionDateByPaidAt
	}
	if !domain.IsTransactionDateByValue(q.DateBy) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid date basis", "data": nil})
		return
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
	// Date-only parameters use UTC calendar days, independent of server
	// timezone (ADR 0039): a UTC day ends at 07:00 WIB. Absent bounds default
	// to the summary window (today and the preceding 29 days).
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from, to := today.AddDate(0, 0, -29), today
	if raw, present := c.GetQuery("date_from"); present {
		if raw == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid transaction date range", "data": nil})
			return
		}
		from, err = time.Parse("2006-01-02", raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid transaction date range", "data": nil})
			return
		}
	}
	if raw, present := c.GetQuery("date_to"); present {
		if raw == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid transaction date range", "data": nil})
			return
		}
		to, err = time.Parse("2006-01-02", raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid transaction date range", "data": nil})
			return
		}
	}
	if from.After(to) || to.AddDate(0, 0, 1).Sub(from) > 366*24*time.Hour {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid transaction date range", "data": nil})
		return
	}
	q.DateFrom, q.DateTo = &from, &to
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="transactions-`+from.Format("2006-01-02")+`_to_`+to.Format("2006-01-02")+`.csv"`)
	if _, err := h.txUsecase.ExportTransactions(c.Request.Context(), tenantID, q, c.Writer); err != nil {
		// KEL-61: the raw error may carry internal details, so it is logged
		// server-side. When the first batch already flushed, the 200 headers
		// are gone and only truncating the stream is left.
		slog.ErrorContext(c.Request.Context(), "transaction export failed", "error", err)
		if !c.Writer.Written() {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to export transactions", "data": nil})
			return
		}
		c.Writer.Flush()
	}
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
			// KEL-61: the raw usecase error may carry internal details (driver
			// messages, constraint names), so it is logged server-side and the
			// client only ever sees a fixed generic message.
			slog.ErrorContext(c.Request.Context(), "enrollment payment cancellation failed", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to cancel transaction", "data": nil})
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
// @Description Internal service-to-service endpoint for generating a billing invoice. The platform fee is computed from the applied platform fee policy (KEL-99); the request's platform_fee is ignored.
// @Tags Billing
// @Accept json
// @Produce json
// @Param request body domain.GenerateSubscriptionPaymentRequest true "Payment request details"
// @Success 201 {object} domain.HTTPResponse{data=domain.GenerateSubscriptionPaymentResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 422 {object} domain.ErrorResponse "code platform_fee_exceeds_gross"
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse "code platform_fee_policy_unavailable"
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
	if errors.Is(err, domain.ErrInvalidPaymentMethod) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error(), "data": nil})
		return
	}
	if errors.Is(err, domain.ErrVoucherRejected) {
		c.JSON(422, gin.H{"status": "error", "code": domain.VoucherRejectedCode, "message": domain.VoucherRejectedMessage, "data": nil})
		return
	}
	if errors.Is(err, domain.ErrPlatformFeeExceedsGross) {
		// KEL-99: a stable machine-readable code so callers can tell this apart
		// from other validation failures. No record was written.
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"status":  "error",
			"code":    domain.PlatformFeeExceedsGrossCode,
			"message": domain.PlatformFeeExceedsGrossMessage,
			"data":    nil,
		})
		return
	}
	if errors.Is(err, domain.ErrPlatformFeePolicyUnavailable) || errors.Is(err, domain.ErrPlatformFeeAmountOutOfRange) {
		// KEL-99: fail closed. Without a trustworthy applied fee policy no
		// transaction is created, and the request can be retried later.
		slog.ErrorContext(c.Request.Context(), "subscription payment refused: platform fee policy", "error", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":  "error",
			"code":    "platform_fee_policy_unavailable",
			"message": "Platform fee policy is unavailable",
			"data":    nil,
		})
		return
	}
	if err != nil {
		// KEL-61: the raw usecase error may carry internal details (driver
		// messages, constraint names), so it is logged server-side and the
		// client only ever sees a fixed generic message.
		slog.ErrorContext(c.Request.Context(), "subscription payment generation failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "Failed to generate subscription payment",
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
		slog.WarnContext(c.Request.Context(), "invalid Duitku callback signature", "request_id", middleware.RequestID(c.Request.Context()))
		c.JSON(http.StatusUnauthorized, gin.H{
			"status":  "error",
			"message": "Invalid Duitku callback signature",
		})
		return
	}

	if err := h.txUsecase.HandleDuitkuWebhook(c.Request.Context(), &payload); err != nil {
		if errors.Is(err, domain.ErrInvalidWebhookSignature) {
			slog.WarnContext(c.Request.Context(), "invalid Duitku callback signature", "request_id", middleware.RequestID(c.Request.Context()))
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
