package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/identity"
)

// RequireActivePlatform checks the live assignment and factor version on every call.
// The signed claim alone is not sufficient after an assignment is revoked.
func RequireActivePlatform(client identity.PlatformAdminClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.GetString("user_id"))
		v, _ := c.Get("platform_factor_version")
		version, _ := v.(int64)
		if !c.GetBool("is_platform_admin") || id == uuid.Nil || err != nil || version <= 0 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"status": "error", "message": "Platform access required", "data": nil})
			return
		}
		allowed, err := client.CheckActive(c.Request.Context(), id.String(), version)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Authorization service unavailable", "data": nil})
			return
		}
		if !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"status": "error", "message": "Platform access required", "data": nil})
			return
		}
		c.Next()
	}
}
