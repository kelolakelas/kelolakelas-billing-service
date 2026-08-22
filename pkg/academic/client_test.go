package academic

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestActivateEnrollmentUsesPutAndInternalCredential(t *testing.T) {
	credential := "billing-academic-secret"
	enrollmentID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/internal/enrollments/"+enrollmentID.String()+"/activate" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content type=%q", got)
		}
		if got := r.Header.Get("X-Internal-Service-Credential"); got != credential {
			t.Fatalf("credential=%q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := NewClient(server.URL, credential).ActivateEnrollment(t.Context(), enrollmentID); err != nil {
		t.Fatal(err)
	}
}

func TestActivateEnrollmentRedactsCredentialFromError(t *testing.T) {
	credential := "secret-not-for-errors"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid ` + credential + `"}`))
	}))
	defer server.Close()

	err := NewClient(server.URL, credential).ActivateEnrollment(t.Context(), uuid.New())
	if err == nil || strings.Contains(err.Error(), credential) || !strings.Contains(err.Error(), "status code 401") {
		t.Fatalf("unexpected error=%v", err)
	}
}
