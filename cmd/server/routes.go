package main

import (
	"github.com/gin-gonic/gin"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/identity"
)

// routeHandlers are the endpoint handlers the billing router mounts. They are passed in
// as plain handler funcs so the route table, including which middleware guards which
// route, can be tested without a database or a payment gateway.
type routeHandlers struct {
	duitkuWebhook          gin.HandlerFunc
	listTransactions       gin.HandlerFunc
	salesSummary           gin.HandlerFunc
	exportTransactions     gin.HandlerFunc
	getTransaction         gin.HandlerFunc
	generateInternal       gin.HandlerFunc
	cancelInternal         gin.HandlerFunc
	listReconciliations    gin.HandlerFunc
	listLifecycle          gin.HandlerFunc
	requeueReconciliations gin.HandlerFunc
	walletBalance          gin.HandlerFunc
	listLedger             gin.HandlerFunc
	listBankAccounts       gin.HandlerFunc
	createBankAccount      gin.HandlerFunc
	updateBankAccount      gin.HandlerFunc
	deleteBankAccount      gin.HandlerFunc
	setPrimaryBankAccount  gin.HandlerFunc
	listVouchers           gin.HandlerFunc
	getVoucher             gin.HandlerFunc
	createVoucher          gin.HandlerFunc
	updateVoucher          gin.HandlerFunc
	deleteVoucher          gin.HandlerFunc
	requestWithdrawal      gin.HandlerFunc
	cancelWithdrawal       gin.HandlerFunc
	getWithdrawal          gin.HandlerFunc
	listWithdrawals        gin.HandlerFunc
	platformWithdrawals    gin.HandlerFunc
	platformMarkPaid       gin.HandlerFunc
	platformReject         gin.HandlerFunc
}

// registerRoutes mounts the public, protected, and internal billing routes.
//
// The browser-facing reads are subject to the `billing:read` permission check
// (KEL-57, ADR 0024), and the wallet and ledger reads (KEL-142) are tenant-only
// like the sales summary: parents are refused before identity or the handler
// run. Payout-account writes (KEL-142) and withdrawal requests and cancels
// (KEL-143) need `billing:withdraw`. Tenant voucher reads, creates, updates,
// and deletes (KEL-161) each need their own `voucher:*` permission and are
// tenant-only like the payout-account routes. The Duitku
// webhook keeps its HMAC check, and the internal routes keep the static
// internal credential; neither is a tenant-member call, so neither asks
// identity anything.
func registerRoutes(r gin.IRouter, h routeHandlers, jwtSecret, internalCredential string, permissions identity.PermissionClient, admins identity.PlatformAdminClient) {
	apiV1 := r.Group("/api/v1/billing")
	apiV1.POST("/webhooks/duitku", h.duitkuWebhook)
	protected := apiV1.Group("")
	protected.Use(middleware.AuthMiddleware(jwtSecret))
	readTransactions := middleware.RequirePermissionUnlessParent(permissions, middleware.PermissionBillingRead)
	protected.GET("/transactions", readTransactions, h.listTransactions)
	protected.GET("/transactions/summary", middleware.RequirePermission(permissions, middleware.PermissionBillingRead), h.salesSummary)
	// The static export path must stay registered before the detail route is
	// read alongside it: gin prefers the static segment, and the explicit row
	// keeps that contract pinned in the route test.
	protected.GET("/transactions/export", middleware.RequirePermission(permissions, middleware.PermissionBillingRead), h.exportTransactions)
	protected.GET("/transactions/:id", readTransactions, h.getTransaction)
	billingRead := middleware.RequirePermission(permissions, middleware.PermissionBillingRead)
	protected.GET("/wallet", billingRead, h.walletBalance)
	protected.GET("/ledger", billingRead, h.listLedger)
	billingWithdraw := middleware.RequirePermission(permissions, middleware.PermissionBillingWithdraw)
	protected.GET("/bank-accounts", billingWithdraw, h.listBankAccounts)
	protected.POST("/bank-accounts", billingWithdraw, h.createBankAccount)
	protected.PATCH("/bank-accounts/:id", billingWithdraw, h.updateBankAccount)
	protected.DELETE("/bank-accounts/:id", billingWithdraw, h.deleteBankAccount)
	protected.POST("/bank-accounts/:id/set-primary", billingWithdraw, h.setPrimaryBankAccount)
	protected.POST("/withdrawals", billingWithdraw, h.requestWithdrawal)
	protected.GET("/withdrawals", billingWithdraw, h.listWithdrawals)
	protected.GET("/withdrawals/:id", billingWithdraw, h.getWithdrawal)
	protected.DELETE("/withdrawals/:id", billingWithdraw, h.cancelWithdrawal)
	// The static collection path must stay registered before the detail route
	// is read alongside it, mirroring the transaction export contract above.
	protected.GET("/vouchers", middleware.RequirePermission(permissions, middleware.PermissionVoucherRead), h.listVouchers)
	protected.POST("/vouchers", middleware.RequirePermission(permissions, middleware.PermissionVoucherCreate), h.createVoucher)
	protected.GET("/vouchers/:id", middleware.RequirePermission(permissions, middleware.PermissionVoucherRead), h.getVoucher)
	protected.PATCH("/vouchers/:id", middleware.RequirePermission(permissions, middleware.PermissionVoucherUpdate), h.updateVoucher)
	protected.DELETE("/vouchers/:id", middleware.RequirePermission(permissions, middleware.PermissionVoucherDelete), h.deleteVoucher)

	platform := r.Group("/api/v1/platform/withdrawals")
	platform.Use(middleware.AuthMiddleware(jwtSecret), middleware.RequireActivePlatform(admins))
	platform.GET("", h.platformWithdrawals)
	platform.POST("/:id/paid", h.platformMarkPaid)
	platform.POST("/:id/reject", h.platformReject)

	internal := r.Group("/internal/billing")
	internal.Use(middleware.InternalServiceAuth(internalCredential))
	internal.POST("/transactions", h.generateInternal)
	internal.POST("/transactions/cancel", h.cancelInternal)
	// Operator-facing recovery path for durable enrollments; there is no platform admin
	// persona, so it stays behind the internal credential instead of the browser.
	internal.GET("/reconciliations", h.listReconciliations)
	internal.GET("/subscription-lifecycle", h.listLifecycle)
	internal.POST("/reconciliations/requeue", h.requeueReconciliations)
}
