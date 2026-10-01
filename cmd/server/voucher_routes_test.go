package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
)

// KEL-161: every voucher route is tenant-only and guarded by its own voucher:*
// permission. Denied or stale memberships never reach the handler; an identity
// failure fails closed with 503. The route table is exercised with real signed
// tokens and the real permission middleware.
func TestVoucherRoutesRequireOwnVoucherPermission(t *testing.T) {
	tenantID := uuid.NewString()
	creatorRole, teacherRole := uuid.NewString(), uuid.NewString()
	removedMember := uuid.NewString()
	stub := &identityStub{voucher: map[string]bool{creatorRole: true}, removed: map[string]bool{removedMember: true}}
	router, rec := newRouteTestRouter(t, stub)
	id := uuid.NewString()
	routes := []struct {
		method, path, name, permission string
	}{
		{http.MethodGet, "/api/v1/billing/vouchers", "voucher-list", middleware.PermissionVoucherRead},
		{http.MethodPost, "/api/v1/billing/vouchers", "voucher-create", middleware.PermissionVoucherCreate},
		{http.MethodGet, "/api/v1/billing/vouchers/" + id, "voucher-get", middleware.PermissionVoucherRead},
		{http.MethodPatch, "/api/v1/billing/vouchers/" + id, "voucher-update", middleware.PermissionVoucherUpdate},
		{http.MethodDelete, "/api/v1/billing/vouchers/" + id, "voucher-delete", middleware.PermissionVoucherDelete},
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
			// The allowed call must have asked identity for this route's own
			// permission inside the token tenant.
			last := stub.calls[len(stub.calls)-1]
			if want := tenantID + "|" + creatorRole + "|"; !strings.HasPrefix(last, want) || !strings.HasSuffix(last, "|"+route.permission) {
				t.Fatalf("identity call=%q, want tenant|role|member|%s", last, route.permission)
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
		if !strings.HasPrefix(call, tenantID+"|") || !strings.Contains(call, middleware.PermissionVoucherRead) && !strings.Contains(call, middleware.PermissionVoucherCreate) && !strings.Contains(call, middleware.PermissionVoucherUpdate) && !strings.Contains(call, middleware.PermissionVoucherDelete) {
			t.Fatalf("unexpected identity check: %s", call)
		}
	}
	stub.err = errors.New("identity down")
	if got := serve(router, http.MethodPost, "/api/v1/billing/vouchers", creator, nil); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("identity down status=%d, want 503", got.Code)
	}
	if rec.hits["voucher-create"] != 1 {
		t.Fatal("identity outage reached the voucher create handler")
	}
	// The static collection path must not be captured by the detail route.
	stub.err = nil
	if got := serve(router, http.MethodGet, "/api/v1/billing/vouchers", creator, nil); got.Code != http.StatusOK || rec.hits["voucher-list"] != 2 || rec.hits["voucher-get"] != 1 {
		t.Fatalf("collection hits=%v, want the list handler and not the detail route", rec.hits)
	}
}
