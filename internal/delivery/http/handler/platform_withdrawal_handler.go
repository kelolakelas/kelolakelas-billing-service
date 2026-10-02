package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

type PlatformWithdrawalHandler struct {
	decisions usecase.PlatformWithdrawalUsecase
}

func NewPlatformWithdrawalHandler(decisions usecase.PlatformWithdrawalUsecase) *PlatformWithdrawalHandler {
	return &PlatformWithdrawalHandler{decisions: decisions}
}

// ListRequested godoc
// @Summary List pending withdrawals or decision history
// @Description Active platform admins only; default returns the oldest pending withdrawals. status=decided returns paid and rejected decisions newest first. Returns the frozen full payout destination.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param page query int false "Page number"
// @Param page_size query int false "Page size, maximum 100"
// @Param status query string false "Set to decided for paid/rejected history; omit for requested queue" Enums(decided)
// @Success 200 {object} domain.HTTPResponse{data=domain.PlatformWithdrawalListResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/platform/withdrawals [get]
func (h *PlatformWithdrawalHandler) ListRequested(c *gin.Context) {
	q := domain.WithdrawalQuery{Page: 1, PageSize: 20}
	for key, target := range map[string]*int{"page": &q.Page, "page_size": &q.PageSize} {
		if c.Query(key) != "" {
			n, err := strconv.Atoi(c.Query(key))
			if err != nil || n < 1 || key == "page_size" && n > 100 {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid pagination", "data": nil})
				return
			}
			*target = n
		}
	}
	status := c.Query("status")
	if status != "" && status != "decided" || len(c.Request.URL.Query()["status"]) > 0 && status == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid status", "data": nil})
		return
	}
	var result *domain.PlatformWithdrawalListResponse
	var err error
	if status == "decided" {
		result, err = h.decisions.ListDecided(c.Request.Context(), q)
	} else {
		result, err = h.decisions.ListRequested(c.Request.Context(), q)
	}
	if err != nil {
		mapWithdrawalError(c, err, "platform withdrawal list failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "data": result})
}

// MarkPaid godoc
// @Summary Record manual withdrawal payment
// @Description Active platform admins only. Records a unique external transfer reference and consumes the held balance atomically.
// @Tags Billing
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Withdrawal UUID"
// @Param request body domain.MarkWithdrawalPaidRequest true "Transfer reference"
// @Success 200 {object} domain.HTTPResponse{data=domain.PlatformWithdrawalResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/platform/withdrawals/{id}/paid [post]
func (h *PlatformWithdrawalHandler) MarkPaid(c *gin.Context) {
	var req domain.MarkWithdrawalPaidRequest
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.TransferReference) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Transfer reference required", "data": nil})
		return
	}
	h.decide(c, domain.WithdrawalStatusPaid, req.TransferReference)
}

// Reject godoc
// @Summary Reject a manual withdrawal
// @Description Active platform admins only. Returns held balance to available and records the rejection reason atomically.
// @Tags Billing
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Withdrawal UUID"
// @Param request body domain.RejectWithdrawalRequest true "Reason"
// @Success 200 {object} domain.HTTPResponse{data=domain.PlatformWithdrawalResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/platform/withdrawals/{id}/reject [post]
func (h *PlatformWithdrawalHandler) Reject(c *gin.Context) {
	var req domain.RejectWithdrawalRequest
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.Reason) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Reason required", "data": nil})
		return
	}
	h.decide(c, domain.WithdrawalStatusRejected, req.Reason)
}

func (h *PlatformWithdrawalHandler) decide(c *gin.Context, decision, detail string) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid withdrawal ID", "data": nil})
		return
	}
	adminID, err := uuid.Parse(c.GetString("user_id"))
	if err != nil || adminID == uuid.Nil {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "message": "Platform access required", "data": nil})
		return
	}
	result, err := h.decisions.Decide(c.Request.Context(), adminID, id, decision, detail)
	if err != nil {
		if errors.Is(err, domain.ErrWithdrawalDuplicateReference) {
			c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "Transfer reference already used", "data": nil})
			return
		}
		mapWithdrawalError(c, err, "platform withdrawal decision failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "data": result})
}
