package email

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func TestResendClientSendOmitsReplyTo(t *testing.T) {
	var payload map[string]interface{}
	client := NewResendClient("resend-test-key", "billing@example.com").(*ResendClient)
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}

	if err := client.Send(context.Background(), domain.EmailMessage{To: "parent@example.com", Subject: "Payment reminder", HTML: "<p>Pay</p>"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["reply_to"]; ok {
		t.Fatal("request payload unexpectedly contains reply_to")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
