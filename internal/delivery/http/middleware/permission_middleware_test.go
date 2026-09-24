package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type permissionClientStub struct {
	allowed  bool
	err      error
	calls    int
	tenantID string
	roleID   string
	name     string
}

func (s *permissionClientStub) CheckPermission(_ context.Context, tenantID, roleID, permission string) (bool, error) {
	s.calls++
	s.tenantID, s.roleID, s.name = tenantID, roleID, permission
	return s.allowed, s.err
}

func (*permissionClientStub) Close() error { return nil }

// KEL-57: the transaction reads serve both tenant members and parents. A parent passes
// on ownership alone; every other caller needs billing:read inside its own tenant, and
// an unusable authorization service must never fall through to the data.
func TestRequirePermissionUnlessParent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	roleID := uuid.New().String()
	tenantID := uuid.New().String()
	cases := []struct {
		name       string
		tenantID   string
		roleID     string
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
		{name: "tenant token without role_id is denied before lookup", tenantID: tenantID, wantStatus: http.StatusForbidden},
		{name: "malformed role_id is denied before lookup", tenantID: tenantID, roleID: "not-a-uuid", wantStatus: http.StatusForbidden},
		{name: "token without tenant is denied before lookup", roleID: roleID, wantStatus: http.StatusForbidden},
		{name: "role without billing:read is denied", tenantID: tenantID, roleID: roleID, wantStatus: http.StatusForbidden, wantLookup: true},
		{name: "role with billing:read is allowed", tenantID: tenantID, roleID: roleID, allowed: true, wantStatus: http.StatusNoContent, wantCalled: true, wantLookup: true},
		{name: "identity error is 503", tenantID: tenantID, roleID: roleID, clientErr: errors.New("deadline exceeded"), wantStatus: http.StatusServiceUnavailable, wantLookup: true},
		{name: "missing client is 503", tenantID: tenantID, roleID: roleID, nilClient: true, wantStatus: http.StatusServiceUnavailable},
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
			if tc.wantLookup && (stub.tenantID != tc.tenantID || stub.roleID != tc.roleID || stub.name != "billing:read") {
				t.Fatalf("identity asked (%s,%s,%s), want (%s,%s,billing:read)", stub.tenantID, stub.roleID, stub.name, tc.tenantID, tc.roleID)
			}
			if !tc.wantCalled && response.Body.String() == "" {
				t.Fatal("a refused request must carry the error envelope")
			}
		})
	}
}
