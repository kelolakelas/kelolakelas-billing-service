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

// WalletHandler serves the tenant wallet balance and ledger mutation reads
// (KEL-142). Both routes sit behind `billing:read` and refuse parent tokens,
// following the SalesSummary handler: parents have their own transaction reads
// and never see the tenant wallet.
type WalletHandler struct {
	wallets usecase.WalletUsecase
}

func NewWalletHandler(wallets usecase.WalletUsecase) *WalletHandler {
	return &WalletHandler{wallets: wallets}
}

// GetBalance godoc
// @Summary Get tenant wallet balance
// @Description Tenant members with billing:read only; parents are forbidden. A tenant without a wallet gets a zero balance, not an error: sandbox callbacks never credit the wallet or the ledger. On payment-only data the balance equals the sum of the tenant's ledger entries.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Success 200 {object} domain.HTTPResponse{data=domain.WalletBalanceResponse}
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/wallet [get]
func (h *WalletHandler) GetBalance(c *gin.Context) {
	if c.GetBool("is_parent") {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "message": "Permission denied", "data": nil})
		return
	}
	tenantID, err := uuid.Parse(c.GetString("tenant_id"))
	if err != nil || tenantID == uuid.Nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid tenant context", "data": nil})
		return
	}
	balance, err := h.wallets.GetBalance(c.Request.Context(), tenantID)
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "wallet balance failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to fetch wallet balance", "data": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Wallet balance fetched successfully", "data": balance})
}

// ListLedger godoc
// @Summary List tenant ledger mutations
// @Description Tenant members with billing:read only; parents are forbidden. Ledger entries are append-only financial evidence, listed newest first. A tenant without a wallet gets an empty page, not an error.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param page query int false "Page number, starting at 1"
// @Param page_size query int false "Entries per page, at most 100"
// @Success 200 {object} domain.HTTPResponse{data=domain.LedgerListResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/ledger [get]
func (h *WalletHandler) ListLedger(c *gin.Context) {
	if c.GetBool("is_parent") {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "message": "Permission denied", "data": nil})
		return
	}
	tenantID, err := uuid.Parse(c.GetString("tenant_id"))
	if err != nil || tenantID == uuid.Nil {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid tenant context", "data": nil})
		return
	}
	query := domain.LedgerQuery{Page: 1, PageSize: 20}
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
	result, err := h.wallets.ListLedger(c.Request.Context(), tenantID, query)
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "ledger list failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to fetch ledger entries", "data": nil})
		return
	}
	if result.Items == nil {
		result.Items = []domain.LedgerEntryResponse{}
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Ledger entries fetched successfully", "data": result})
}

// BankAccountHandler serves the tenant payout-account management (KEL-142).
// Every route sits behind `billing:withdraw` and refuses parent tokens: payout
// destinations belong to the tenant, and parents never manage them.
type BankAccountHandler struct {
	accounts usecase.BankAccountUsecase
}

func NewBankAccountHandler(accounts usecase.BankAccountUsecase) *BankAccountHandler {
	return &BankAccountHandler{accounts: accounts}
}

func bankAccountID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid bank account ID", "data": nil})
		return uuid.Nil, false
	}
	return id, true
}

func bankAccountTenant(c *gin.Context) (uuid.UUID, bool) {
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

func mapBankAccountError(c *gin.Context, err error, action string) {
	switch {
	case errors.Is(err, domain.ErrBankAccountNotFound):
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "Bank account not found", "data": nil})
	case errors.Is(err, domain.ErrBankAccountInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid bank account request", "data": nil})
	case errors.Is(err, domain.ErrBankAccountInUse):
		c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "Bank account is referenced by an active withdrawal", "data": nil})
	case errors.Is(err, domain.ErrBankAccountConflict):
		c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "Primary account conflict, please retry", "data": nil})
	default:
		// KEL-61: the raw error may carry driver details, so it is logged
		// server-side and the client only sees a fixed generic message.
		slog.ErrorContext(c.Request.Context(), action, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to manage bank account", "data": nil})
	}
}

// ListBankAccounts godoc
// @Summary List tenant payout bank accounts
// @Description Tenant members with billing:withdraw only; parents are forbidden. Account numbers are always masked to the last four digits. The primary account is listed first.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Success 200 {object} domain.HTTPResponse{data=domain.BankAccountListResponse}
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/bank-accounts [get]
func (h *BankAccountHandler) ListBankAccounts(c *gin.Context) {
	tenantID, ok := bankAccountTenant(c)
	if !ok {
		return
	}
	result, err := h.accounts.List(c.Request.Context(), tenantID)
	if err != nil {
		mapBankAccountError(c, err, "bank account list failed")
		return
	}
	if result.Items == nil {
		result.Items = []domain.BankAccountResponse{}
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Bank accounts fetched successfully", "data": result})
}

// CreateBankAccount godoc
// @Summary Create a tenant payout bank account
// @Description Tenant members with billing:withdraw only; parents are forbidden. The first account of a tenant always becomes primary. Account ownership is verified manually by the platform admin at payout processing time; the service only validates shape, never ownership.
// @Tags Billing
// @Accept json
// @Produce json
// @Param request body domain.CreateBankAccountRequest true "Bank account details"
// @Success 201 {object} domain.HTTPResponse{data=domain.BankAccountResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/bank-accounts [post]
func (h *BankAccountHandler) CreateBankAccount(c *gin.Context) {
	tenantID, ok := bankAccountTenant(c)
	if !ok {
		return
	}
	var req domain.CreateBankAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid bank account request", "data": nil})
		return
	}
	created, err := h.accounts.Create(c.Request.Context(), tenantID, &req)
	if err != nil {
		mapBankAccountError(c, err, "bank account create failed")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "success", "message": "Bank account created successfully", "data": created})
}

// UpdateBankAccount godoc
// @Summary Update a tenant payout bank account
// @Description Tenant members with billing:withdraw only; parents are forbidden. Only the supplied fields change; the primary slot never moves here, use the set-primary endpoint for that.
// @Tags Billing
// @Accept json
// @Produce json
// @Param id path string true "Bank account UUID"
// @Param request body domain.UpdateBankAccountRequest true "Fields to update"
// @Success 200 {object} domain.HTTPResponse{data=domain.BankAccountResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/bank-accounts/{id} [patch]
func (h *BankAccountHandler) UpdateBankAccount(c *gin.Context) {
	tenantID, ok := bankAccountTenant(c)
	if !ok {
		return
	}
	id, ok := bankAccountID(c)
	if !ok {
		return
	}
	var req domain.UpdateBankAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid bank account request", "data": nil})
		return
	}
	updated, err := h.accounts.Update(c.Request.Context(), tenantID, id, &req)
	if err != nil {
		mapBankAccountError(c, err, "bank account update failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Bank account updated successfully", "data": updated})
}

// DeleteBankAccount godoc
// @Summary Delete a tenant payout bank account
// @Description Tenant members with billing:withdraw only; parents are forbidden. The delete is a soft delete: history rows are never removed. A primary account still backing a withdrawal in an active status answers 409.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param id path string true "Bank account UUID"
// @Success 200 {object} domain.HTTPResponse
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/bank-accounts/{id} [delete]
func (h *BankAccountHandler) DeleteBankAccount(c *gin.Context) {
	tenantID, ok := bankAccountTenant(c)
	if !ok {
		return
	}
	id, ok := bankAccountID(c)
	if !ok {
		return
	}
	if err := h.accounts.Delete(c.Request.Context(), tenantID, id); err != nil {
		mapBankAccountError(c, err, "bank account delete failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Bank account deleted successfully", "data": nil})
}

// SetPrimaryBankAccount godoc
// @Summary Set the tenant primary payout bank account
// @Description Tenant members with billing:withdraw only; parents are forbidden. The primary slot moves without deleting any row, so payout history survives the change. Concurrent promoters serialize on the storage backstop and the loser answers 409.
// @Tags Billing
// @Produce json
// @Security BearerAuth
// @Param id path string true "Bank account UUID"
// @Success 200 {object} domain.HTTPResponse{data=domain.BankAccountResponse}
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 403 {object} domain.ErrorResponse
// @Failure 404 {object} domain.ErrorResponse
// @Failure 409 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Router /api/v1/billing/bank-accounts/{id}/set-primary [post]
func (h *BankAccountHandler) SetPrimaryBankAccount(c *gin.Context) {
	tenantID, ok := bankAccountTenant(c)
	if !ok {
		return
	}
	id, ok := bankAccountID(c)
	if !ok {
		return
	}
	primary, err := h.accounts.SetPrimary(c.Request.Context(), tenantID, id)
	if err != nil {
		mapBankAccountError(c, err, "bank account set-primary failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Primary bank account updated successfully", "data": primary})
}
