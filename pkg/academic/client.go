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
	UpdateEnrollmentStatus(ctx context.Context, enrollmentID uuid.UUID, status string) error
}

type client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) Client {
	if baseURL == "" {
		baseURL = "http://localhost:8081"
	}
	return &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *client) UpdateEnrollmentStatus(ctx context.Context, enrollmentID uuid.UUID, status string) error {
	endpoint := fmt.Sprintf("%s/api/v1/enrollments/%s/status", c.baseURL, enrollmentID.String())

	body := map[string]string{
		"status": status,
	}
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

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
