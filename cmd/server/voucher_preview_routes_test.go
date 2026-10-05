package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKEL162PreviewRequiresInternalCredential(t *testing.T) {
	stub := &identityStub{}
	router, rec := newRouteTestRouter(t, stub)
	for _, credential := range []string{"", "wrong", routeTestCredential} {
		req := httptest.NewRequest(http.MethodPost, "/internal/billing/vouchers/preview", nil)
		req.Header.Set("X-Internal-Service-Credential", credential)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		want := 401
		if credential == routeTestCredential {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("credential=%q code=%d want=%d", credential, w.Code, want)
		}
	}
	if rec.hits["preview-voucher"] != 1 || len(stub.calls) != 0 {
		t.Fatalf("hits=%v identity=%v", rec.hits, stub.calls)
	}
}
