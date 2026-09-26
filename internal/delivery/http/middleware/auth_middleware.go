package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func InternalServiceAuth(credential string) gin.HandlerFunc {
	return func(c *gin.Context) {
		expected := sha256.Sum256([]byte(credential))
		provided := sha256.Sum256([]byte(c.GetHeader("X-Internal-Service-Credential")))
		if credential == "" || subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
			c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid internal service credential", "data": nil})
			c.Abort()
			return
		}
		c.Next()
	}
}

type Claims struct {
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
	RoleID   string `json:"role_id"`
	MemberID string `json:"member_id"`
	IsParent bool   `json:"is_parent"`
	jwt.RegisteredClaims
}

func AuthMiddleware(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Authorization required", "data": nil})
			c.Abort()
			return
		}
		claims := &Claims{}
		token, err := jwt.ParseWithClaims(parts[1], claims, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrTokenSignatureInvalid
			}
			return []byte(secret), nil
		})
		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid or expired token", "data": nil})
			c.Abort()
			return
		}
		c.Set("user_id", claims.UserID)
		c.Set("tenant_id", claims.TenantID)
		c.Set("role_id", claims.RoleID)
		c.Set("member_id", claims.MemberID)
		c.Set("is_parent", claims.IsParent)
		c.Next()
	}
}
