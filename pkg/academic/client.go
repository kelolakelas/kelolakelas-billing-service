package academic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/requestid"
)

// Client performs the enrollment side effects billing owes Academic. Each call is
// idempotent on the Academic side so the durable reconciliation worker can retry it
// safely after a timeout or an outage.
type Client interface {
	ActivateEnrollment(ctx context.Context, enrollmentID uuid.UUID) error
	ReleaseEnrollment(ctx context.Context, enrollmentID uuid.UUID) error
}

type client struct {
	baseURL    string
	credential string
	httpClient *http.Client
}

func NewClient(baseURL, credential string) Client {
	if baseURL == "" {
		baseURL = "http://localhost:8081"
	}
	return &client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		credential: credential,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *client) ActivateEnrollment(ctx context.Context, enrollmentID uuid.UUID) error {
	return c.callEnrollment(ctx, enrollmentID, "activate")
}

// ReleaseEnrollment returns the seat held by an enrollment whose payment failed or
// expired. Academic answers 200 for an enrollment that is already released, so a
// retried release is a no-op instead of an error.
func (c *client) ReleaseEnrollment(ctx context.Context, enrollmentID uuid.UUID) error {
	return c.callEnrollment(ctx, enrollmentID, "release")
}

func (c *client) callEnrollment(ctx context.Context, enrollmentID uuid.UUID, action string) error {
	endpoint := fmt.Sprintf("%s/internal/enrollments/%s/%s", c.baseURL, enrollmentID.String(), action)
	body := map[string]string{}
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Credential", c.credential)
	if id := requestid.FromContext(ctx); id != "" {
		req.Header.Set("X-Request-ID", id)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request to academic service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil || envelope.Message == "" {
			return fmt.Errorf("academic service returned status code %d", resp.StatusCode)
		}
		return fmt.Errorf("academic service returned status code %d: %s", resp.StatusCode, redactCredential(envelope.Message, c.credential))
	}

	return nil
}

func redactCredential(message, credential string) string {
	if credential == "" {
		return message
	}
	return strings.ReplaceAll(message, credential, "[redacted]")
}
