package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

// WithdrawalHandler serves the tenant withdrawal request and cancel flow
// (KEL-143). Every route sits behind `billing:withdraw` and refuses parent
// tokens, like the payout-account routes: withdrawals move the tenant's
// balance, and parents never touch it.
type WithdrawalHandler struct {
	withdrawals usecase.WithdrawalUsecase
}

func NewWithdrawalHandler(withdrawals usecase.WithdrawalUsecase) *WithdrawalHandler {
	return &WithdrawalHandler{withdrawals: withdrawals}
}

func withdrawalTenant(c *gin.Context) (uuid.UUID, bool) {
	if c.GetBool("is_parent") {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "message": "Permission denied", "data": nil})
		return uuid.Nil, false
	}
	tenantID, err := uuid.Parse(c.GetString("tenant_id"))
	if err != nil || tenantID == uuid.Nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid tenant context", "data": nil})
		return uuid.Nil, false
	}
	return tenantID, true
}

func withdrawalID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid withdrawal ID", "data": nil})
		return uuid.Nil, false
	}
	return id, true
}

func mapWithdrawalError(c *gin.Context, err error, action string) {
	switch {
	case errors.Is(err, domain.ErrWithdrawalInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid withdrawal request", "data": nil})
	case errors.Is(err, domain.ErrWithdrawalBelowMinimum):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "message": "Withdrawal amount below minimum", "data": nil})
	case errors.Is(err, domain.ErrWithdrawalInsufficientBalance):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "message": "Insufficient available balance", "data": nil})
	case errors.Is(err, domain.ErrWithdrawalNoPrimaryAccount):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "message": "Tenant has no primary bank account", "data": nil})
	case errors.Is(err, domain.ErrWithdrawalOpenExists):
		c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "An open withdrawal request already exists", "data": nil})
	case errors.Is(err, domain.ErrWithdrawalIdempotencyMismatch):
		c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "Idempotency key already used with a different request", "data": nil})
	case errors.Is(err, domain.ErrWithdrawalNotFound):
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "Withdrawal not found", "data": nil})
	case errors.Is(err, domain.ErrWithdrawalInvalidState):
		c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "Withdrawal cannot be cancelled in its current status", "data": nil})
	default:
		// KEL-61: the raw error may carry driver details, so it is logged
		// server-side and the client only sees a fixed generic message.
		slog.ErrorContext(c.Request.Context(), action, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to process withdrawal", "data": nil})
	}
}

// RequestWithdrawal godoc
// @Summary Request a tenant payout withdrawal
// @Description Tenant members with billing:withdraw only; parents are forbidden. Moves the amount from the available balance to the held balance in one transaction and freezes the payout destination at request time. A retry carrying the same idempotency key returns the stored request without moving balance twice. Only one open request per tenant.
// @Tags Billing
// @Accept json
// @Produce json
// @Param request body domain.RequestWithdrawalRequest true "Withdrawal details"
// @Success 201 {object} domain.HTTPResponse{data=domain.WithdrawalResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 422 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/withdrawals [post]
func (h *WithdrawalHandler) RequestWithdrawal(c *gin.Context) {
	tenantID, ok := withdrawalTenant(c)
	if !ok {
		return
	}
	var req domain.RequestWithdrawalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid withdrawal request", "data": nil})
		return
	}
	input := domain.RequestWithdrawalInput{Amount: req.Amount, IdempotencyKey: req.IdempotencyKey}
	if req.BankAccountID != "" {
		accountID, err := uuid.Parse(req.BankAccountID)
		if err != nil || accountID == uuid.Nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid withdrawal request", "data": nil})
			return
		}
		input.BankAccountID = &accountID
	}
	created, err := h.withdrawals.RequestWithdrawal(c.Request.Context(), tenantID, input)
	if err != nil {
		mapWithdrawalError(c, err, "withdrawal request failed")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "success", "message": "Withdrawal requested successfully", "data": created})
}

// CancelWithdrawal godoc
// @Summary Cancel a tenant payout withdrawal request
// @Description Tenant members with billing:withdraw only; parents are forbidden. Only a request still in `requested` can be cancelled; the held amount returns to the available balance with a reversal ledger entry. Of concurrent cancellers exactly one wins.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param id path string true "Withdrawal UUID"
// @Success 200 {object} domain.HTTPResponse{data=domain.WithdrawalResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/withdrawals/{id} [delete]
func (h *WithdrawalHandler) CancelWithdrawal(c *gin.Context) {
	tenantID, ok := withdrawalTenant(c)
	if !ok {
		return
	}
	id, ok := withdrawalID(c)
	if !ok {
		return
	}
	cancelled, err := h.withdrawals.CancelWithdrawal(c.Request.Context(), tenantID, id)
	if err != nil {
		mapWithdrawalError(c, err, "withdrawal cancel failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Withdrawal cancelled successfully", "data": cancelled})
}

// GetWithdrawal godoc
// @Summary Get one tenant payout withdrawal request
// @Description Tenant members with billing:withdraw only; parents are forbidden. The destination is the snapshot frozen at request time, with the account number masked.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param id path string true "Withdrawal UUID"
// @Success 200 {object} domain.HTTPResponse{data=domain.WithdrawalResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/withdrawals/{id} [get]
func (h *WithdrawalHandler) GetWithdrawal(c *gin.Context) {
	tenantID, ok := withdrawalTenant(c)
	if !ok {
		return
	}
	id, ok := withdrawalID(c)
	if !ok {
		return
	}
	found, err := h.withdrawals.GetWithdrawal(c.Request.Context(), tenantID, id)
	if err != nil {
		mapWithdrawalError(c, err, "withdrawal fetch failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Withdrawal fetched successfully", "data": found})
}

// ListWithdrawals godoc
// @Summary List tenant payout withdrawal requests
// @Description Tenant members with billing:withdraw only; parents are forbidden. Withdrawals are listed newest first with masked destination account numbers.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param page query int false "Page number, starting at 1"
// @Param page_size query int false "Entries per page, at most 100"
// @Success 200 {object} domain.HTTPResponse{data=domain.WithdrawalListResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/withdrawals [get]
func (h *WithdrawalHandler) ListWithdrawals(c *gin.Context) {
	tenantID, ok := withdrawalTenant(c)
	if !ok {
		return
	}
	query := domain.WithdrawalQuery{Page: 1, PageSize: 20}
	for key, target := range map[string]*int{"page": &query.Page, "page_size": &query.PageSize} {
		if v := c.Query(key); v != "" {
			n, parseErr := strconv.Atoi(v)
			if parseErr != nil || n < 1 || (key == "page_size" && n > 100) {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid pagination", "data": nil})
				return
			}
			*target = n
		}
	}
	result, err := h.withdrawals.ListWithdrawals(c.Request.Context(), tenantID, query)
	if err != nil {
		mapWithdrawalError(c, err, "withdrawal list failed")
		return
	}
	if result.Items == nil {
		result.Items = []domain.WithdrawalResponse{}
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Withdrawals fetched successfully", "data": result})
}
