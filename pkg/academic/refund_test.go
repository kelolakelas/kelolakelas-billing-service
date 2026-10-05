package academic

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefundEnrollmentStatesAndExactRequest(t *testing.T) {
	for _, state := range []string{"active", "suspended", "pending", "dropped", "completed", "activation_race", "unavailable", "invalid_response"} {
		t.Run(state, func(t *testing.T) {
			id := uuid.New()
			calls := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != "PUT" || string(body) != "{}" || r.Header.Get("X-Internal-Service-Credential") != "test-credential" {
					t.Errorf("request %s %s body=%q", r.Method, r.URL.Path, body)
				}
				if !strings.HasPrefix(r.URL.Path, "/internal/enrollments/"+id.String()+"/") {
					t.Errorf("wrong enrollment %s", r.URL.Path)
				}
				action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				calls = append(calls, action)
				if state == "unavailable" {
					w.WriteHeader(503)
					return
				}
				if state == "invalid_response" {
					fmt.Fprint(w, `{"data":{}}`)
					return
				}
				if state == "completed" || ((state == "pending" || state == "activation_race") && len(calls) == 1) {
					w.WriteHeader(409)
					return
				}
				status := "dropped"
				if state == "activation_race" && action == "release" {
					status = "active"
				}
				fmt.Fprintf(w, `{"status":"success","data":{"status":%q}}`, status)
			}))
			defer server.Close()
			err := NewClient(server.URL, "test-credential").(RefundClient).EndRefundedEnrollment(context.Background(), id)
			if state == "unavailable" || state == "invalid_response" {
				if err == nil {
					t.Fatal("expected retryable failure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "end"
			if state == "pending" || state == "completed" {
				want = "end,release"
			}
			if state == "activation_race" {
				want = "end,release,end"
			}
			if strings.Join(calls, ",") != want {
				t.Fatalf("calls %v want %s", calls, want)
			}
		})
	}
}
