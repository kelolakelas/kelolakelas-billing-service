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
	getTransaction         gin.HandlerFunc
	generateInternal       gin.HandlerFunc
	cancelInternal         gin.HandlerFunc
	listReconciliations    gin.HandlerFunc
	requeueReconciliations gin.HandlerFunc
}

// registerRoutes mounts the public, protected, and internal billing routes.
//
// Only the two browser-facing transaction reads are subject to the `billing:read`
// permission check (KEL-57, ADR 0024). The Duitku webhook keeps its HMAC check, and the
// internal routes keep the static internal credential; neither is a tenant-member call,
// so neither asks identity anything.
func registerRoutes(r gin.IRouter, h routeHandlers, jwtSecret, internalCredential string, permissions identity.PermissionClient) {
	apiV1 := r.Group("/api/v1/billing")
	apiV1.POST("/webhooks/duitku", h.duitkuWebhook)
	protected := apiV1.Group("")
	protected.Use(middleware.AuthMiddleware(jwtSecret))
	readTransactions := middleware.RequirePermissionUnlessParent(permissions, middleware.PermissionBillingRead)
	protected.GET("/transactions", readTransactions, h.listTransactions)
	protected.GET("/transactions/:id", readTransactions, h.getTransaction)

	internal := r.Group("/internal/billing")
	internal.Use(middleware.InternalServiceAuth(internalCredential))
	internal.POST("/transactions", h.generateInternal)
	internal.POST("/transactions/cancel", h.cancelInternal)
	// Operator-facing recovery path for durable enrollments; there is no platform admin
	// persona, so it stays behind the internal credential instead of the browser.
	internal.GET("/reconciliations", h.listReconciliations)
	internal.POST("/reconciliations/requeue", h.requeueReconciliations)
}
