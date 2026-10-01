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

// VoucherHandler serves the tenant voucher management (KEL-161). Every route
// sits behind its own `voucher:*` permission and refuses parent tokens:
// vouchers belong to the tenant, and parents never manage them. Checkout
// redemption is out of scope and lives in KEL-29.
type VoucherHandler struct {
	vouchers usecase.VoucherUsecase
}

func NewVoucherHandler(vouchers usecase.VoucherUsecase) *VoucherHandler {
	return &VoucherHandler{vouchers: vouchers}
}

func voucherTenant(c *gin.Context) (uuid.UUID, bool) {
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

func voucherID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid voucher ID", "data": nil})
		return uuid.Nil, false
	}
	return id, true
}

func mapVoucherError(c *gin.Context, err error, action string) {
	switch {
	case errors.Is(err, domain.ErrVoucherNotFound):
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "Voucher not found", "data": nil})
	case errors.Is(err, domain.ErrVoucherInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid voucher request", "data": nil})
	case errors.Is(err, domain.ErrVoucherDuplicate):
		c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "Voucher code already exists", "data": nil})
	case errors.Is(err, domain.ErrVoucherInUse):
		c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "Voucher has been used and can only be deactivated", "data": nil})
	default:
		// KEL-61: the raw error may carry driver details, so it is logged
		// server-side and the client only sees a fixed generic message.
		slog.ErrorContext(c.Request.Context(), action, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to manage voucher", "data": nil})
	}
}

// ListVouchers godoc
// @Summary List tenant vouchers
// @Description Tenant members with voucher:read only; parents are forbidden. Vouchers are listed newest first with their usage counts.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param page query int false "Page number, starting at 1"
// @Param page_size query int false "Entries per page, at most 100"
// @Success 200 {object} domain.HTTPResponse{data=domain.VoucherListResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/vouchers [get]
func (h *VoucherHandler) ListVouchers(c *gin.Context) {
	tenantID, ok := voucherTenant(c)
	if !ok {
		return
	}
	query := domain.VoucherQuery{Page: 1, PageSize: 20}
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
	result, err := h.vouchers.List(c.Request.Context(), tenantID, query)
	if err != nil {
		mapVoucherError(c, err, "voucher list failed")
		return
	}
	if result.Items == nil {
		result.Items = []domain.VoucherResponse{}
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Vouchers fetched successfully", "data": result})
}

// GetVoucher godoc
// @Summary Get one tenant voucher
// @Description Tenant members with voucher:read only; parents are forbidden. A voucher of another tenant answers 404, never its row.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param id path string true "Voucher UUID"
// @Success 200 {object} domain.HTTPResponse{data=domain.VoucherResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/vouchers/{id} [get]
func (h *VoucherHandler) GetVoucher(c *gin.Context) {
	tenantID, ok := voucherTenant(c)
	if !ok {
		return
	}
	id, ok := voucherID(c)
	if !ok {
		return
	}
	found, err := h.vouchers.Get(c.Request.Context(), tenantID, id)
	if err != nil {
		mapVoucherError(c, err, "voucher fetch failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Voucher fetched successfully", "data": found})
}

// CreateVoucher godoc
// @Summary Create a tenant voucher
// @Description Tenant members with voucher:create only; parents are forbidden. The code is unique case-insensitively per tenant: a percentage takes 1-100, a nominal takes a positive amount, the date range must be ordered, and the usage cap must cover what was already consumed.
// @Tags Billing
// @Accept json
// @Produce json
// @Param request body domain.CreateVoucherRequest true "Voucher details"
// @Success 201 {object} domain.HTTPResponse{data=domain.VoucherResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/vouchers [post]
func (h *VoucherHandler) CreateVoucher(c *gin.Context) {
	tenantID, ok := voucherTenant(c)
	if !ok {
		return
	}
	var req domain.CreateVoucherRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid voucher request", "data": nil})
		return
	}
	created, err := h.vouchers.Create(c.Request.Context(), tenantID, &req)
	if err != nil {
		mapVoucherError(c, err, "voucher create failed")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "success", "message": "Voucher created successfully", "data": created})
}

// UpdateVoucher godoc
// @Summary Update a tenant voucher
// @Description Tenant members with voucher:update only; parents are forbidden. Only the supplied fields change; a rename colliding with another live voucher of the tenant answers 409. Lowering the usage cap below what was already consumed answers 400, and reactivating an expired voucher is allowed.
// @Tags Billing
// @Accept json
// @Produce json
// @Param id path string true "Voucher UUID"
// @Param request body domain.UpdateVoucherRequest true "Fields to update"
// @Success 200 {object} domain.HTTPResponse{data=domain.VoucherResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/vouchers/{id} [patch]
func (h *VoucherHandler) UpdateVoucher(c *gin.Context) {
	tenantID, ok := voucherTenant(c)
	if !ok {
		return
	}
	id, ok := voucherID(c)
	if !ok {
		return
	}
	var req domain.UpdateVoucherRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid voucher request", "data": nil})
		return
	}
	updated, err := h.vouchers.Update(c.Request.Context(), tenantID, id, &req)
	if err != nil {
		mapVoucherError(c, err, "voucher update failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Voucher updated successfully", "data": updated})
}

// DeleteVoucher godoc
// @Summary Delete a tenant voucher
// @Description Tenant members with voucher:delete only; parents are forbidden. The delete is a soft delete: history rows are never removed. A voucher that already discounted a transaction answers 409 and can only be deactivated, never deleted.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param id path string true "Voucher UUID"
// @Success 200 {object} domain.HTTPResponse
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/vouchers/{id} [delete]
func (h *VoucherHandler) DeleteVoucher(c *gin.Context) {
	tenantID, ok := voucherTenant(c)
	if !ok {
		return
	}
	id, ok := voucherID(c)
	if !ok {
		return
	}
	if err := h.vouchers.Delete(c.Request.Context(), tenantID, id); err != nil {
		mapVoucherError(c, err, "voucher delete failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Voucher deleted successfully", "data": nil})
}
