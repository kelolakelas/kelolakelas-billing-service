package email

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func TestResendClientSendOmitsReplyTo(t *testing.T) {
	var payload map[string]interface{}
	var idempotencyKey string
	client := NewResendClient("resend-test-key", "billing@example.com").(*ResendClient)
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		idempotencyKey = request.Header.Get("Idempotency-Key")
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}

	if err := client.Send(context.Background(), domain.EmailMessage{To: "parent@example.com", Subject: "Payment reminder", HTML: "<p>Pay</p>", IdempotencyKey: "private-payment/test"}); err != nil {
		t.Fatal(err)
	}
	if idempotencyKey != "private-payment/test" {
		t.Fatalf("idempotency key = %q", idempotencyKey)
	}
	if _, ok := payload["reply_to"]; ok {
		t.Fatal("request payload unexpectedly contains reply_to")
	}
}

func TestResendClientMissingConfigurationFailsWithoutNetwork(t *testing.T) {
	for _, tc := range []struct{ key, from, to string }{
		{"", "billing@example.test", "parent@example.test"},
		{"key", "", "parent@example.test"},
		{"key", "billing@example.test", ""},
	} {
		client := NewResendClient(tc.key, tc.from).(*ResendClient)
		client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unexpected email request")
			return nil, nil
		})
		if err := client.Send(context.Background(), domain.EmailMessage{To: tc.to}); err == nil {
			t.Fatalf("missing configuration appeared successful: %+v", tc)
		}
	}
}

func TestResendClientTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)
	client := NewResendClient("test-key", "billing@example.com", 25*time.Millisecond).(*ResendClient)
	// Route the fixed Resend endpoint to the local slow server without changing production URL.
	client.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return http.DefaultTransport.RoundTrip(req)
	})
	start := time.Now()
	err := client.Send(context.Background(), domain.EmailMessage{To: "parent@example.com", Subject: "Payment link"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v, want deadline exceeded", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("slow Resend request was not bounded")
	}
}

func TestResendClientDefaultTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		client := NewResendClient("test-key", "billing@example.com", timeout).(*ResendClient)
		if client.httpClient.Timeout != 10*time.Second {
			t.Fatalf("timeout = %v, want 10s", client.httpClient.Timeout)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
