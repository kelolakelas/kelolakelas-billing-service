package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type ResendClient struct {
	apiKey, from string
	httpClient   *http.Client
}

func NewResendClient(apiKey, from string, timeout ...time.Duration) domain.EmailClient {
	bound := time.Duration(config.DefaultProviderHTTPTimeoutSeconds) * time.Second
	if len(timeout) > 0 && timeout[0] > 0 {
		bound = timeout[0]
	}
	return &ResendClient{apiKey: apiKey, from: from, httpClient: &http.Client{Timeout: bound}}
}

func (c *ResendClient) Send(ctx context.Context, message domain.EmailMessage) error {
	if c.apiKey == "" || c.from == "" || message.To == "" {
		return nil
	}
	payload := map[string]interface{}{"from": c.from, "to": []string{message.To}, "subject": message.Subject, "html": message.HTML}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("email provider returned status %d", resp.StatusCode)
	}
	return nil
}
