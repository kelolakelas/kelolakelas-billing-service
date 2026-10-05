package academic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/requestid"
)

type RefundClient interface {
	EndRefundedEnrollment(context.Context, uuid.UUID) error
}

// A 409 on end needs release; release on active is a no-op, so its response
// must be inspected before considering the refund's enrollment effect complete.
func (c *client) EndRefundedEnrollment(ctx context.Context, id uuid.UUID) error {
	status, code, err := c.refundCall(ctx, id, "end")
	if err != nil {
		return err
	}
	if code == http.StatusConflict {
		status, code, err = c.refundCall(ctx, id, "release")
		if err != nil {
			return err
		}
		if code == http.StatusConflict {
			return nil
		} // completed is intentionally untouched
		if status == "active" || status == "suspended" {
			status, code, err = c.refundCall(ctx, id, "end")
			if err != nil {
				return err
			}
		}
	}
	if code >= 200 && code < 300 && status == "dropped" {
		return nil
	}
	return fmt.Errorf("academic refund transition not complete (status %d, enrollment %s)", code, status)
}

func (c *client) refundCall(ctx context.Context, id uuid.UUID, action string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/internal/enrollments/%s/%s", c.baseURL, id, action), bytes.NewBufferString("{}"))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Credential", c.credential)
	if rid := requestid.FromContext(ctx); rid != "" {
		req.Header.Set("X-Request-ID", rid)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("academic refund request failed: %s", redactCredential(err.Error(), c.credential))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return "", resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, fmt.Errorf("academic service returned status code %d", resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return "", resp.StatusCode, fmt.Errorf("invalid academic refund response")
	}
	return envelope.Data.Status, resp.StatusCode, nil
}
