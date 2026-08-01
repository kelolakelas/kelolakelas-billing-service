package domain

import (
	"encoding/json"
	"fmt"
)

type FlipWebhookPayload struct {
	Data  interface{} `json:"data" form:"data"`
	Token string      `json:"token" form:"token"`
}

type FlipBillData struct {
	ID          string `json:"id"`
	BillLinkID  string `json:"bill_link_id"`
	BillLink    string `json:"bill_link"`
	BillTitle   string `json:"bill_title"`
	SenderName  string `json:"sender_name"`
	SenderBank  string `json:"sender_bank"`
	SenderEmail string `json:"sender_email"`
	Amount      int64  `json:"amount"`
	Status      string `json:"status"` // "SUCCESS", "FAILED", "PENDING"
	Timestamp   string `json:"timestamp"`
}

func (p *FlipWebhookPayload) ParseBillData() (*FlipBillData, error) {
	if p == nil || p.Data == nil {
		return nil, fmt.Errorf("empty webhook payload data")
	}

	switch v := p.Data.(type) {
	case string:
		var billData FlipBillData
		if err := json.Unmarshal([]byte(v), &billData); err != nil {
			return nil, fmt.Errorf("failed to parse string data into bill data: %w", err)
		}
		return &billData, nil
	case map[string]interface{}:
		bytes, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal map data: %w", err)
		}
		var billData FlipBillData
		if err := json.Unmarshal(bytes, &billData); err != nil {
			return nil, fmt.Errorf("failed to parse map data into bill data: %w", err)
		}
		return &billData, nil
	default:
		bytes, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal interface data: %w", err)
		}
		var billData FlipBillData
		if err := json.Unmarshal(bytes, &billData); err != nil {
			return nil, fmt.Errorf("failed to parse data into bill data: %w", err)
		}
		return &billData, nil
	}
}
