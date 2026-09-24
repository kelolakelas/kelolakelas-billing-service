package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/pkg/identity"
)

// PermissionBillingRead is the identity permission that lets a tenant member read the
// tenant's transactions. Identity seeds it for the Creator role only.
const PermissionBillingRead = "billing:read"

// RequirePermissionUnlessParent guards the transaction reads, which serve both tenant
// members and parents (KEL-57, ADR 0024). A parent token carries ownership rather than
// a role, so the handler's `parent_id` scope stays the only authority for that caller
// and identity is never consulted for it. Every other caller must hold the permission
// inside the tenant resolved from its verified JWT claim. It must run after
// AuthMiddleware, which puts the claims on the context.
//
// The semantics match academic's permission middleware (ADR 0002): a missing role or
// tenant claim, or a denial, is 403; an unusable or unreachable authorization service
// is 503. Neither failure lets the request reach the handler, so no data is served.
func RequirePermissionUnlessParent(client identity.PermissionClient, permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetBool("is_parent") {
			c.Next()
			return
		}
		if !permissionAllowed(c, client, permission) {
			c.Abort()
			return
		}
		c.Next()
	}
}

// permissionAllowed writes the failure response and reports false when the caller must
// not proceed.
func permissionAllowed(c *gin.Context, client identity.PermissionClient, permission string) bool {
	if client == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Authorization service unavailable", "data": nil})
		return false
	}
	// Tokens issued before the member had a role, or tenant tokens without a role claim,
	// cannot be authorized and are rejected before identity is asked.
	roleID, err := uuid.Parse(c.GetString("role_id"))
	if err != nil || roleID == uuid.Nil {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "message": "Permission denied", "data": nil})
		return false
	}
	tenantID, err := uuid.Parse(c.GetString("tenant_id"))
	if err != nil || tenantID == uuid.Nil {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "message": "Permission denied", "data": nil})
		return false
	}
	allowed, err := client.CheckPermission(c.Request.Context(), tenantID.String(), roleID.String(), permission)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Authorization service unavailable", "data": nil})
		return false
	}
	if !allowed {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "message": "Permission denied", "data": nil})
		return false
	}
	return true
}
