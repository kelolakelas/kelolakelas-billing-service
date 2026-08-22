package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type ResendClient struct {
	apiKey, from string
	httpClient   *http.Client
}

func NewResendClient(apiKey, from string) domain.EmailClient {
	return &ResendClient{apiKey: apiKey, from: from, httpClient: &http.Client{}}
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
