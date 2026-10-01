package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
)

type platformStub struct {
	allowed bool
	err     error
	calls   int
	user    string
	version int64
}

func (s *platformStub) CheckActive(_ context.Context, user string, version int64) (bool, error) {
	s.calls++
	s.user = user
	s.version = version
	return s.allowed, s.err
}
func (*platformStub) Close() error { return nil }

func TestPlatformWithdrawalRoutesFailClosed(t *testing.T) {
	adminID := uuid.NewString()
	admin := signToken(t, middleware.Claims{UserID: adminID, IsPlatformAdmin: true, PlatformFactorVersion: 3})
	tenant := signToken(t, middleware.Claims{UserID: uuid.NewString(), TenantID: uuid.NewString()})
	paths := []struct{ method, path, hit string }{
		{http.MethodGet, "/api/v1/platform/withdrawals", "platform-withdrawal-list"},
		{http.MethodPost, "/api/v1/platform/withdrawals/" + uuid.NewString() + "/paid", "platform-withdrawal-paid"},
		{http.MethodPost, "/api/v1/platform/withdrawals/" + uuid.NewString() + "/reject", "platform-withdrawal-reject"},
	}
	for _, route := range paths {
		t.Run(route.hit, func(t *testing.T) {
			stub := &platformStub{allowed: true}
			// Direct route-table construction exercises the real JWT and live check.
			r, rec := newPlatformTestRouter(stub)
			if got := serve(r, route.method, route.path, admin, nil); got.Code != 200 {
				t.Fatalf("active status=%d", got.Code)
			}
			if stub.calls != 1 || stub.user != adminID || stub.version != 3 {
				t.Fatalf("identity call=%+v", stub)
			}
			stub.allowed = false
			if got := serve(r, route.method, route.path, admin, nil); got.Code != 403 {
				t.Fatalf("revoked status=%d", got.Code)
			}
			stub.err = errors.New("identity down")
			if got := serve(r, route.method, route.path, admin, nil); got.Code != 503 {
				t.Fatalf("outage status=%d", got.Code)
			}
			if got := serve(r, route.method, route.path, tenant, nil); got.Code != 403 {
				t.Fatalf("tenant status=%d", got.Code)
			}
			if got := serve(r, route.method, route.path, "", nil); got.Code != 401 {
				t.Fatalf("anonymous status=%d", got.Code)
			}
			if rec.hits[route.hit] != 1 {
				t.Fatalf("handler hits=%d", rec.hits[route.hit])
			}
		})
	}
}
