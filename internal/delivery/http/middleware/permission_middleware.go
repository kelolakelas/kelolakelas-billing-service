package middleware

import (
	"log/slog"
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
// The semantics match academic's permission middleware (ADR 0002): a missing role,
// tenant, or member_id claim, or a denial, is 403; an unusable or unreachable
// authorization service is 503. Neither failure lets the request reach the handler,
// so no data is served.
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
func permissionFailure(c *gin.Context, status int) {
	message := "Permission denied"
	if status == http.StatusServiceUnavailable {
		message = "Authorization service unavailable"
	}
	slog.WarnContext(c.Request.Context(), "permission check failed", "request_id", RequestID(c.Request.Context()), "status", status)
	c.JSON(status, gin.H{"status": "error", "message": message, "data": nil})
}

func permissionAllowed(c *gin.Context, client identity.PermissionClient, permission string) bool {
	if client == nil {
		permissionFailure(c, http.StatusServiceUnavailable)
		return false
	}
	// Tokens issued before the member had a role, or tenant tokens without a role claim,
	// cannot be authorized and are rejected before identity is asked.
	roleID, err := uuid.Parse(c.GetString("role_id"))
	if err != nil || roleID == uuid.Nil {
		permissionFailure(c, http.StatusForbidden)
		return false
	}
	tenantID, err := uuid.Parse(c.GetString("tenant_id"))
	if err != nil || tenantID == uuid.Nil {
		permissionFailure(c, http.StatusForbidden)
		return false
	}
	// KEL-80: the check is pinned to the membership the verified token was issued for, so
	// identity can deny a member who was removed or moved to another role while the token
	// is still valid. A tenant token without a usable member_id claim cannot be pinned and
	// is rejected here, before identity is consulted.
	memberID, err := uuid.Parse(c.GetString("member_id"))
	if err != nil || memberID == uuid.Nil {
		permissionFailure(c, http.StatusForbidden)
		return false
	}
	allowed, err := client.CheckPermission(OutgoingContext(c.Request.Context()), tenantID.String(), roleID.String(), memberID.String(), permission)
	if err != nil {
		permissionFailure(c, http.StatusServiceUnavailable)
		return false
	}
	if !allowed {
		permissionFailure(c, http.StatusForbidden)
		return false
	}
	return true
}
