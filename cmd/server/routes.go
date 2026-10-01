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
	getTransaction         gin.HandlerFunc
	generateInternal       gin.HandlerFunc
	cancelInternal         gin.HandlerFunc
	listReconciliations    gin.HandlerFunc
	requeueReconciliations gin.HandlerFunc
	walletBalance          gin.HandlerFunc
	listLedger             gin.HandlerFunc
	listBankAccounts       gin.HandlerFunc
	createBankAccount      gin.HandlerFunc
	updateBankAccount      gin.HandlerFunc
	deleteBankAccount      gin.HandlerFunc
	setPrimaryBankAccount  gin.HandlerFunc
	requestWithdrawal      gin.HandlerFunc
	cancelWithdrawal       gin.HandlerFunc
	getWithdrawal          gin.HandlerFunc
	listWithdrawals        gin.HandlerFunc
}

// registerRoutes mounts the public, protected, and internal billing routes.
//
// The browser-facing reads are subject to the `billing:read` permission check
// (KEL-57, ADR 0024), and the wallet and ledger reads (KEL-142) are tenant-only
// like the sales summary: parents are refused before identity or the handler
// run. Payout-account writes (KEL-142) and withdrawal requests and cancels
// (KEL-143) need `billing:withdraw`. The Duitku
// webhook keeps its HMAC check, and the internal routes keep the static
// internal credential; neither is a tenant-member call, so neither asks
// identity anything.
func registerRoutes(r gin.IRouter, h routeHandlers, jwtSecret, internalCredential string, permissions identity.PermissionClient) {
	apiV1 := r.Group("/api/v1/billing")
	apiV1.POST("/webhooks/duitku", h.duitkuWebhook)
	protected := apiV1.Group("")
	protected.Use(middleware.AuthMiddleware(jwtSecret))
	readTransactions := middleware.RequirePermissionUnlessParent(permissions, middleware.PermissionBillingRead)
	protected.GET("/transactions", readTransactions, h.listTransactions)
	protected.GET("/transactions/summary", middleware.RequirePermission(permissions, middleware.PermissionBillingRead), h.salesSummary)
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

	internal := r.Group("/internal/billing")
	internal.Use(middleware.InternalServiceAuth(internalCredential))
	internal.POST("/transactions", h.generateInternal)
	internal.POST("/transactions/cancel", h.cancelInternal)
	// Operator-facing recovery path for durable enrollments; there is no platform admin
	// persona, so it stays behind the internal credential instead of the browser.
	internal.GET("/reconciliations", h.listReconciliations)
	internal.POST("/reconciliations/requeue", h.requeueReconciliations)
}
