package academic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Client interface {
	ActivateEnrollment(ctx context.Context, enrollmentID uuid.UUID) error
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
	endpoint := fmt.Sprintf("%s/internal/enrollments/%s/activate", c.baseURL, enrollmentID.String())
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

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request to academic service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("academic service returned status code %d", resp.StatusCode)
	}

	return nil
}
