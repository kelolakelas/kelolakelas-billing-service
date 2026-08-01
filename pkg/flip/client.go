package flip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Client interface {
	CreateBill(ctx context.Context, req *CreateBillRequest) (*CreateBillResponse, error)
}

type client struct {
	baseURL    string
	secretKey  string
	httpClient *http.Client
}

type CreateBillRequest struct {
	Title       string `json:"title"`
	Type        string `json:"type"`
	Amount      *int64 `json:"amount,omitempty"`
	ReferenceID string `json:"reference_id,omitempty"`
	RedirectURL string `json:"redirect_url,omitempty"`
	Step        string `json:"step"`
	SenderName  string `json:"sender_name,omitempty"`
	SenderEmail string `json:"sender_email,omitempty"`
	SenderPhone string `json:"sender_phone_number,omitempty"`
}

type CreateBillResponse struct {
	LinkID  string `json:"link_id"`
	LinkURL string `json:"link_url"`
	Title   string `json:"title"`
	Amount  int64  `json:"amount"`
	Status  string `json:"status"`
}

func NewClient(baseURL, secretKey string) Client {
	if baseURL == "" {
		baseURL = "https://bigflip.id/big_sandbox_api"
	}
	return &client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		secretKey: secretKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *client) CreateBill(ctx context.Context, req *CreateBillRequest) (*CreateBillResponse, error) {
	endpoint := c.baseURL + "/v3/pwf/bill"
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to encode flip request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.SetBasicAuth(c.secretKey, "")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request to flip api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Message string `json:"message"`
			Errors  any    `json:"errors"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return nil, fmt.Errorf("flip api error (status %d): %s", resp.StatusCode, errResp.Message)
	}

	var flipResp CreateBillResponse
	if err := json.NewDecoder(resp.Body).Decode(&flipResp); err != nil {
		return nil, fmt.Errorf("failed to decode flip response: %w", err)
	}

	return &flipResp, nil
}
