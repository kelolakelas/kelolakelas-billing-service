package main

import (
	"errors"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
	"strings"
	"testing"
)

func TestRefundRouteRequiresPermission(t *testing.T) {
	tenant, role := uuid.NewString(), uuid.NewString()
	for _, tc := range []struct {
		name            string
		allowed, parent bool
		failure         error
		want            int
	}{{"allowed", true, false, nil, 200}, {"denied", false, false, nil, 403}, {"parent", true, true, nil, 403}, {"identity unavailable", false, false, errors.New("offline"), 503}} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &identityStub{withdraw: map[string]bool{role: tc.allowed}, err: tc.failure}
			router, rec := newRouteTestRouter(t, stub)
			token := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: tenant, RoleID: role, MemberID: uuid.NewString(), IsParent: tc.parent})
			response := serve(router, "POST", "/api/v1/billing/transactions/"+uuid.NewString()+"/refund", token, nil)
			if response.Code != tc.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.want != 200 && rec.hits["refund"] != 0 {
				t.Fatal("denied request reached handler")
			}
			if !tc.parent && (len(stub.calls) != 1 || !strings.HasSuffix(stub.calls[0], "|billing:refund")) {
				t.Fatalf("permission %v", stub.calls)
			}
		})
	}
}
