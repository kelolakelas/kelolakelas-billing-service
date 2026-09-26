package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type permissionClientStub struct {
	allowed  bool
	err      error
	calls    int
	tenantID string
	roleID   string
	memberID string
	name     string
}

func (s *permissionClientStub) CheckPermission(_ context.Context, tenantID, roleID, memberID, permission string) (bool, error) {
	s.calls++
	s.tenantID, s.roleID, s.memberID, s.name = tenantID, roleID, memberID, permission
	return s.allowed, s.err
}

func (*permissionClientStub) Close() error { return nil }

// KEL-57: the transaction reads serve both tenant members and parents. A parent passes
// on ownership alone; every other caller needs billing:read inside its own tenant, and
// an unusable authorization service must never fall through to the data. KEL-80 pins the
// question to the member_id of the verified token.
func TestRequirePermissionUnlessParent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	roleID := uuid.New().String()
	tenantID := uuid.New().String()
	memberID := uuid.New().String()
	cases := []struct {
		name       string
		tenantID   string
		roleID     string
		memberID   string
		isParent   bool
		allowed    bool
		clientErr  error
		nilClient  bool
		wantStatus int
		wantCalled bool
		wantLookup bool
	}{
		{name: "parent passes without role or tenant", isParent: true, wantStatus: http.StatusNoContent, wantCalled: true},
		{name: "parent passes while identity is down", isParent: true, clientErr: errors.New("identity down"), wantStatus: http.StatusNoContent, wantCalled: true},
		// ADR 0002 counts a token with is_parent as a parent even when it also carries
		// tenant membership claims; KEL-80 must not start asking identity for it.
		{name: "parent with tenant membership claims skips identity", isParent: true, tenantID: tenantID, roleID: roleID, memberID: memberID, wantStatus: http.StatusNoContent, wantCalled: true},
		{name: "parent with an invalid member_id skips identity", isParent: true, tenantID: tenantID, roleID: roleID, memberID: "not-a-uuid", wantStatus: http.StatusNoContent, wantCalled: true},
		{name: "tenant token without role_id is denied before lookup", tenantID: tenantID, memberID: memberID, wantStatus: http.StatusForbidden},
		{name: "malformed role_id is denied before lookup", tenantID: tenantID, roleID: "not-a-uuid", memberID: memberID, wantStatus: http.StatusForbidden},
		{name: "token without tenant is denied before lookup", roleID: roleID, memberID: memberID, wantStatus: http.StatusForbidden},
		{name: "tenant token without member_id is denied before lookup", tenantID: tenantID, roleID: roleID, allowed: true, wantStatus: http.StatusForbidden},
		{name: "non-UUID member_id is denied before lookup", tenantID: tenantID, roleID: roleID, memberID: "member-7", allowed: true, wantStatus: http.StatusForbidden},
		{name: "nil member_id is denied before lookup", tenantID: tenantID, roleID: roleID, memberID: uuid.Nil.String(), allowed: true, wantStatus: http.StatusForbidden},
		{name: "role without billing:read (or removed member) is denied", tenantID: tenantID, roleID: roleID, memberID: memberID, wantStatus: http.StatusForbidden, wantLookup: true},
		{name: "role with billing:read is allowed", tenantID: tenantID, roleID: roleID, memberID: memberID, allowed: true, wantStatus: http.StatusNoContent, wantCalled: true, wantLookup: true},
		{name: "identity error is 503", tenantID: tenantID, roleID: roleID, memberID: memberID, clientErr: errors.New("deadline exceeded"), wantStatus: http.StatusServiceUnavailable, wantLookup: true},
		{name: "missing client is 503", tenantID: tenantID, roleID: roleID, memberID: memberID, nilClient: true, wantStatus: http.StatusServiceUnavailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &permissionClientStub{allowed: tc.allowed, err: tc.clientErr}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				if tc.roleID != "" {
					c.Set("role_id", tc.roleID)
				}
				if tc.tenantID != "" {
					c.Set("tenant_id", tc.tenantID)
				}
				if tc.memberID != "" {
					c.Set("member_id", tc.memberID)
				}
				c.Set("is_parent", tc.isParent)
				c.Next()
			})
			called := false
			guard := RequirePermissionUnlessParent(stub, PermissionBillingRead)
			if tc.nilClient {
				guard = RequirePermissionUnlessParent(nil, PermissionBillingRead)
			}
			router.GET("/transactions", guard, func(c *gin.Context) {
				called = true
				c.Status(http.StatusNoContent)
			})

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/transactions", nil))

			if response.Code != tc.wantStatus || called != tc.wantCalled {
				t.Fatalf("status=%d handler=%t, want status=%d handler=%t", response.Code, called, tc.wantStatus, tc.wantCalled)
			}
			if (stub.calls > 0) != tc.wantLookup {
				t.Fatalf("identity lookups=%d, want lookup=%t", stub.calls, tc.wantLookup)
			}
			if tc.wantLookup && (stub.tenantID != tc.tenantID || stub.roleID != tc.roleID || stub.memberID != tc.memberID || stub.name != "billing:read") {
				t.Fatalf("identity asked (%s,%s,%s,%s), want (%s,%s,%s,billing:read)", stub.tenantID, stub.roleID, stub.memberID, stub.name, tc.tenantID, tc.roleID, tc.memberID)
			}
			if !tc.wantCalled && response.Body.String() == "" {
				t.Fatal("a refused request must carry the error envelope")
			}
		})
	}
}

// KEL-80: AuthMiddleware must expose the verified member_id claim, and the permission
// check must forward exactly that value; a client-supplied header cannot replace it.
func TestAuthMiddlewareForwardsMemberIDToPermissionCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "kel80-member-id-secret"
	claims := Claims{UserID: uuid.NewString(), TenantID: uuid.NewString(), RoleID: uuid.NewString(), MemberID: uuid.NewString()}
	claims.RegisteredClaims = jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	stub := &permissionClientStub{allowed: true}
	router := gin.New()
	router.GET("/transactions", AuthMiddleware(secret), RequirePermissionUnlessParent(stub, PermissionBillingRead), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/transactions", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Member-ID", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want %d (body=%s)", response.Code, http.StatusNoContent, response.Body.String())
	}
	if stub.memberID != claims.MemberID || stub.tenantID != claims.TenantID || stub.roleID != claims.RoleID {
		t.Fatalf("identity asked (tenant=%s role=%s member=%s), want claims (%s,%s,%s)", stub.tenantID, stub.roleID, stub.memberID, claims.TenantID, claims.RoleID, claims.MemberID)
	}
}
