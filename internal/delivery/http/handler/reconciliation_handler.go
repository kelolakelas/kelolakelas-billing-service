package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

type ReconciliationHandler struct {
	reconciliationUsecase usecase.ReconciliationAdminUsecase
}

func NewReconciliationHandler(reconciliationUsecase usecase.ReconciliationAdminUsecase) *ReconciliationHandler {
	return &ReconciliationHandler{reconciliationUsecase: reconciliationUsecase}
}

// ListReconciliations godoc
// @Summary List payment reconciliations by status
// @Description Internal service-to-service endpoint for operators. Returns the durable reconciliation jobs of enrollment activation and seat release so a permanently failed job can be found without a provider callback. Without `status` every row is returned.
// @Tags Billing
// @Produce json
// @Param status query string false "Reconciliation status: pending, processing, active, or terminal_failed"
// @Success 200 {object} domain.HTTPResponse{data=domain.ReconciliationListResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /internal/billing/reconciliations [get]
func (h *ReconciliationHandler) ListReconciliations(c *gin.Context) {
	result, err := h.reconciliationUsecase.ListReconciliations(c.Request.Context(), c.Query("status"))
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidReconciliationStatus):
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid reconciliation status", "data": nil})
		case errors.Is(err, domain.ErrReconciliationUnavailable):
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Reconciliation store is unavailable", "data": nil})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to list reconciliations", "data": nil})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Reconciliations fetched successfully", "data": result})
}

// RequeueTerminalFailedReconciliations godoc
// @Summary Requeue permanently failed payment reconciliations
// @Description Internal service-to-service endpoint for operators. Moves reconciliation jobs that exhausted the attempt limit back to `pending` so the worker retries them without a provider callback. The transition is idempotent: only `terminal_failed` rows are affected, so an already active or in-flight job is never reset.
// @Tags Billing
// @Produce json
// @Success 200 {object} domain.HTTPResponse{data=domain.ReconciliationRequeueResponse}
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /internal/billing/reconciliations/requeue [post]
func (h *ReconciliationHandler) RequeueTerminalFailedReconciliations(c *gin.Context) {
	result, err := h.reconciliationUsecase.RequeueTerminalFailedReconciliations(c.Request.Context())
	if err != nil {
		if errors.Is(err, domain.ErrReconciliationUnavailable) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Reconciliation store is unavailable", "data": nil})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to requeue reconciliations", "data": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Reconciliations requeued successfully", "data": result})
}
