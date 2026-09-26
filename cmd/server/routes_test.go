package main

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

	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
)

const (
	routeTestSecret     = "route-test-jwt-secret-at-least-32-chars"
	routeTestCredential = "route-test-internal-credential"
)

// identityStub answers CheckPermission the way identity would for the seeded roles: the
// Creator role holds every permission, the Teacher role holds none of billing's. Since
// KEL-76 identity also denies a member_id whose membership was removed or no longer
// carries the token's role; the removed set models those memberships.
type identityStub struct {
	grants  map[string]bool // role_id -> holds billing:read
	removed map[string]bool // member_id -> membership no longer active with that role
	err     error
	calls   []string
}

func (s *identityStub) CheckPermission(_ context.Context, tenantID, roleID, memberID, permission string) (bool, error) {
	s.calls = append(s.calls, tenantID+"|"+roleID+"|"+memberID+"|"+permission)
	if s.err != nil {
		return false, s.err
	}
	if s.removed[memberID] {
		return false, nil
	}
	return permission == middleware.PermissionBillingRead && s.grants[roleID], nil
}

func (*identityStub) Close() error { return nil }

type routeRecorder struct{ hits map[string]int }

func (r *routeRecorder) handler(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		r.hits[name]++
		c.JSON(http.StatusOK, gin.H{"status": "success", "data": gin.H{"route": name}})
	}
}

func newRouteTestRouter(t *testing.T, stub *identityStub) (*gin.Engine, *routeRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := &routeRecorder{hits: map[string]int{}}
	router := gin.New()
	registerRoutes(router, routeHandlers{
		duitkuWebhook:          rec.handler("webhook"),
		listTransactions:       rec.handler("list"),
		getTransaction:         rec.handler("get"),
		generateInternal:       rec.handler("internal-generate"),
		cancelInternal:         rec.handler("internal-cancel"),
		listReconciliations:    rec.handler("internal-reconciliations"),
		requeueReconciliations: rec.handler("internal-requeue"),
	}, routeTestSecret, routeTestCredential, stub)
	return router, rec
}

func signToken(t *testing.T, claims middleware.Claims) string {
	t.Helper()
	claims.RegisteredClaims = jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(routeTestSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}

func serve(router *gin.Engine, method, target, token string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// KEL-57 acceptance criteria, exercised through the real route table and middleware chain.
func TestTransactionReadsRequireBillingReadForTenantMembers(t *testing.T) {
	tenantID := uuid.NewString()
	creatorRole := uuid.NewString()
	teacherRole := uuid.NewString()
	customRole := uuid.NewString()
	stub := &identityStub{grants: map[string]bool{creatorRole: true, customRole: true}}
	router, rec := newRouteTestRouter(t, stub)
	transactionPath := "/api/v1/billing/transactions/" + uuid.NewString()

	teacher := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenantID, RoleID: teacherRole, MemberID: uuid.NewString()})
	for _, target := range []string{"/api/v1/billing/transactions", transactionPath} {
		if got := serve(router, http.MethodGet, target, teacher, nil); got.Code != http.StatusForbidden {
			t.Fatalf("Teacher %s status=%d, want 403", target, got.Code)
		}
	}
	if rec.hits["list"] != 0 || rec.hits["get"] != 0 {
		t.Fatalf("denied reads reached the handlers: %v", rec.hits)
	}

	for name, role := range map[string]string{"Creator": creatorRole, "custom role with billing:read": customRole} {
		token := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenantID, RoleID: role, MemberID: uuid.NewString()})
		if got := serve(router, http.MethodGet, "/api/v1/billing/transactions", token, nil); got.Code != http.StatusOK {
			t.Fatalf("%s list status=%d, want 200", name, got.Code)
		}
		if got := serve(router, http.MethodGet, transactionPath, token, nil); got.Code != http.StatusOK {
			t.Fatalf("%s detail status=%d, want 200", name, got.Code)
		}
	}
	for _, call := range stub.calls {
		if call[:len(tenantID)] != tenantID {
			t.Fatalf("identity was asked about another tenant: %s", call)
		}
	}

	// A tenant token issued without a role claim cannot be authorized at all.
	legacy := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenantID, MemberID: uuid.NewString()})
	callsBefore := len(stub.calls)
	if got := serve(router, http.MethodGet, "/api/v1/billing/transactions", legacy, nil); got.Code != http.StatusForbidden {
		t.Fatalf("token without role_id status=%d, want 403", got.Code)
	}
	if len(stub.calls) != callsBefore {
		t.Fatal("a token without role_id must be rejected before identity is asked")
	}
}

// KEL-80 AC2/AC3 through the real route table: the member_id from the verified token
// reaches identity, a removed member (identity denies that member_id even though the role
// still holds billing:read) gets 403, and a tenant token without a usable member_id is
// refused with 403 before identity is asked.
func TestTransactionReadsArePinnedToTheTokenMembership(t *testing.T) {
	tenantID := uuid.NewString()
	creatorRole := uuid.NewString()
	activeMember := uuid.NewString()
	removedMember := uuid.NewString()
	stub := &identityStub{grants: map[string]bool{creatorRole: true}, removed: map[string]bool{removedMember: true}}
	router, rec := newRouteTestRouter(t, stub)

	active := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenantID, RoleID: creatorRole, MemberID: activeMember})
	if got := serve(router, http.MethodGet, "/api/v1/billing/transactions", active, nil); got.Code != http.StatusOK {
		t.Fatalf("active member status=%d, want 200", got.Code)
	}
	if want := tenantID + "|" + creatorRole + "|" + activeMember + "|" + middleware.PermissionBillingRead; len(stub.calls) != 1 || stub.calls[0] != want {
		t.Fatalf("identity calls=%v, want [%s]", stub.calls, want)
	}

	removed := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenantID, RoleID: creatorRole, MemberID: removedMember})
	if got := serve(router, http.MethodGet, "/api/v1/billing/transactions", removed, nil); got.Code != http.StatusForbidden {
		t.Fatalf("removed member status=%d, want 403", got.Code)
	}
	if rec.hits["list"] != 1 {
		t.Fatalf("list hits=%d, want only the active member to reach the handler", rec.hits["list"])
	}

	for name, memberID := range map[string]string{"missing": "", "non-UUID": "member-9", "nil": "00000000-0000-0000-0000-000000000000"} {
		token := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenantID, RoleID: creatorRole, MemberID: memberID})
		callsBefore := len(stub.calls)
		if got := serve(router, http.MethodGet, "/api/v1/billing/transactions", token, nil); got.Code != http.StatusForbidden {
			t.Fatalf("%s member_id status=%d, want 403", name, got.Code)
		}
		if len(stub.calls) != callsBefore {
			t.Fatalf("%s member_id reached identity", name)
		}
	}
	if rec.hits["list"] != 1 {
		t.Fatalf("list hits=%d, refused tokens must not reach the handler", rec.hits["list"])
	}
}

func TestParentTransactionReadsSkipThePermissionCheck(t *testing.T) {
	stub := &identityStub{err: errors.New("identity down")}
	router, rec := newRouteTestRouter(t, stub)
	parent := signToken(t, middleware.Claims{UserID: uuid.NewString(), IsParent: true})

	if got := serve(router, http.MethodGet, "/api/v1/billing/transactions", parent, nil); got.Code != http.StatusOK {
		t.Fatalf("parent list status=%d, want 200 even while identity is down", got.Code)
	}
	if got := serve(router, http.MethodGet, "/api/v1/billing/transactions/"+uuid.NewString(), parent, nil); got.Code != http.StatusOK {
		t.Fatalf("parent detail status=%d, want 200", got.Code)
	}
	if len(stub.calls) != 0 || rec.hits["list"] != 1 || rec.hits["get"] != 1 {
		t.Fatalf("identity calls=%v hits=%v, want the parent path to bypass identity", stub.calls, rec.hits)
	}
}

func TestTransactionReadsFailClosedWhenIdentityIsUnavailable(t *testing.T) {
	stub := &identityStub{err: errors.New("rpc error: code = Unavailable")}
	router, rec := newRouteTestRouter(t, stub)
	member := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: uuid.NewString(), RoleID: uuid.NewString(), MemberID: uuid.NewString()})

	got := serve(router, http.MethodGet, "/api/v1/billing/transactions", member, nil)
	if got.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", got.Code)
	}
	if rec.hits["list"] != 0 {
		t.Fatal("no transaction data may be served while authorization is unavailable")
	}
	if want := `{"data":null,"message":"Authorization service unavailable","status":"error"}`; got.Body.String() != want {
		t.Fatalf("body=%s, want %s", got.Body.String(), want)
	}
}

// The webhook and the internal routes are not tenant-member calls: they keep their own
// guards and must never depend on identity.
func TestWebhookAndInternalRoutesDoNotAskIdentity(t *testing.T) {
	stub := &identityStub{err: errors.New("identity down")}
	router, rec := newRouteTestRouter(t, stub)
	internal := map[string]string{"X-Internal-Service-Credential": routeTestCredential}

	for _, route := range []struct {
		method, target, name string
		headers              map[string]string
	}{
		{http.MethodPost, "/api/v1/billing/webhooks/duitku", "webhook", nil},
		{http.MethodPost, "/internal/billing/transactions", "internal-generate", internal},
		{http.MethodPost, "/internal/billing/transactions/cancel", "internal-cancel", internal},
		{http.MethodGet, "/internal/billing/reconciliations", "internal-reconciliations", internal},
		{http.MethodPost, "/internal/billing/reconciliations/requeue", "internal-requeue", internal},
	} {
		if got := serve(router, route.method, route.target, "", route.headers); got.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d, want 200", route.method, route.target, got.Code)
		}
		if rec.hits[route.name] != 1 {
			t.Fatalf("%s did not reach its handler", route.target)
		}
	}
	if len(stub.calls) != 0 {
		t.Fatalf("identity was consulted for non-member routes: %v", stub.calls)
	}
	if got := serve(router, http.MethodGet, "/internal/billing/reconciliations", "", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("internal route without credential status=%d, want 401", got.Code)
	}
}
