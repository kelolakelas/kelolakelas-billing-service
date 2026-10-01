package main

import (
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
)

// KEL-143: every withdrawal route is tenant-only and guarded by
// billing:withdraw. Denied or stale memberships never reach the handler; an
// identity failure fails closed with 503. The route table is exercised with
// real signed tokens and the real permission middleware.
func TestWithdrawalRoutesRequireBillingWithdraw(t *testing.T) {
	tenantID := uuid.NewString()
	creatorRole, teacherRole := uuid.NewString(), uuid.NewString()
	removedMember := uuid.NewString()
	stub := &identityStub{withdraw: map[string]bool{creatorRole: true}, removed: map[string]bool{removedMember: true}}
	router, rec := newRouteTestRouter(t, stub)
	id := uuid.NewString()
	routes := []struct {
		method, path, name string
	}{
		{http.MethodPost, "/api/v1/billing/withdrawals", "withdrawal-request"},
		{http.MethodGet, "/api/v1/billing/withdrawals", "withdrawal-list"},
		{http.MethodGet, "/api/v1/billing/withdrawals/" + id, "withdrawal-get"},
		{http.MethodDelete, "/api/v1/billing/withdrawals/" + id, "withdrawal-cancel"},
	}
	creator := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenantID, RoleID: creatorRole, MemberID: uuid.NewString()})
	teacher := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenantID, RoleID: teacherRole, MemberID: uuid.NewString()})
	removed := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenantID, RoleID: creatorRole, MemberID: removedMember})
	parent := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenantID, RoleID: creatorRole, MemberID: uuid.NewString(), IsParent: true})
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			if got := serve(router, route.method, route.path, creator, nil); got.Code != http.StatusOK {
				t.Fatalf("Creator status=%d, want 200", got.Code)
			}
			if rec.hits[route.name] != 1 {
				t.Fatalf("%s handler hits=%d, want 1", route.name, rec.hits[route.name])
			}
			for name, token := range map[string]string{"Teacher": teacher, "removed member": removed, "parent": parent} {
				if got := serve(router, route.method, route.path, token, nil); got.Code != http.StatusForbidden {
					t.Fatalf("%s status=%d, want 403", name, got.Code)
				}
			}
			if got := serve(router, route.method, route.path, "", nil); got.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous status=%d, want 401", got.Code)
			}
			if rec.hits[route.name] != 1 {
				t.Fatalf("denied callers reached %s handler", route.name)
			}
		})
	}
	for _, call := range stub.calls {
		if call[:len(tenantID)] != tenantID || call[len(call)-len(middleware.PermissionBillingWithdraw):] != middleware.PermissionBillingWithdraw {
			t.Fatalf("unexpected identity check: %s", call)
		}
	}
	stub.err = errors.New("identity down")
	if got := serve(router, http.MethodPost, "/api/v1/billing/withdrawals", creator, nil); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("identity down status=%d, want 503", got.Code)
	}
	if rec.hits["withdrawal-request"] != 1 {
		t.Fatal("identity outage reached the withdrawal request handler")
	}
}
