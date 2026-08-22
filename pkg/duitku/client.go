package duitku

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type DuitkuAdapter struct {
	baseURL      string
	apiKey       string
	merchantCode string
	httpClient   *http.Client
}

type inquiryRequest struct {
	MerchantCode    string `json:"merchantCode"`
	PaymentAmount   int64  `json:"paymentAmount"`
	PaymentMethod   string `json:"paymentMethod"`
	MerchantOrderID string `json:"merchantOrderId"`
	ProductDetails  string `json:"productDetails"`
	Email           string `json:"email"`
	PhoneNumber     string `json:"phoneNumber,omitempty"`
	CustomerVAName  string `json:"customerVaName"`
	CallbackURL     string `json:"callbackUrl"`
	ReturnURL       string `json:"returnUrl"`
	Signature       string `json:"signature"`
	ExpiryPeriod    int    `json:"expiryPeriod"`
}

type inquiryResponse struct {
	Reference     string `json:"reference"`
	PaymentURL    string `json:"paymentUrl"`
	StatusCode    string `json:"statusCode"`
	StatusMessage string `json:"statusMessage"`
}

func NewClient(baseURL, apiKey, merchantCode string, httpClient *http.Client) *DuitkuAdapter {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &DuitkuAdapter{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, merchantCode: merchantCode, httpClient: httpClient}
}

func (c *DuitkuAdapter) CreateInvoice(ctx context.Context, request *domain.CreateInvoiceRequest) (*domain.CreateInvoiceResponse, error) {
	payload := inquiryRequest{
		MerchantCode: c.merchantCode, PaymentAmount: request.Amount, PaymentMethod: request.PaymentMethod,
		MerchantOrderID: request.MerchantOrderID, ProductDetails: request.ProductDetails, Email: request.Email,
		PhoneNumber: request.PhoneNumber, CustomerVAName: request.CustomerVAName, CallbackURL: request.CallbackURL,
		ReturnURL: request.ReturnURL, Signature: c.requestSignature(request.MerchantOrderID, request.Amount), ExpiryPeriod: request.ExpiryPeriod,
	}
	if payload.ExpiryPeriod == 0 {
		payload.ExpiryPeriod = 1440
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal Duitku inquiry request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v2/inquiry", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("create Duitku inquiry request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("send Duitku inquiry request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read Duitku inquiry response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Duitku inquiry failed with status %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var result inquiryResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("decode Duitku inquiry response: %w", err)
	}
	if result.StatusCode != "00" || result.PaymentURL == "" || result.Reference == "" {
		return nil, fmt.Errorf("Duitku inquiry rejected: %s", result.StatusMessage)
	}
	return &domain.CreateInvoiceResponse{Reference: result.Reference, PaymentURL: result.PaymentURL}, nil
}

func (c *DuitkuAdapter) ValidateCallbackSignature(payload *domain.DuitkuCallbackPayload) bool {
	if payload == nil || payload.MerchantCode == "" || payload.Amount == "" || payload.MerchantOrderID == "" || payload.Signature == "" {
		return false
	}
	if payload.MerchantCode != c.merchantCode {
		return false
	}
	message := payload.MerchantCode + payload.Amount + payload.MerchantOrderID
	return hmac.Equal([]byte(c.signature(message)), []byte(payload.Signature))
}

func (c *DuitkuAdapter) requestSignature(orderID string, amount int64) string {
	return c.signature(c.merchantCode + orderID + strconv.FormatInt(amount, 10))
}

func (c *DuitkuAdapter) signature(message string) string {
	mac := hmac.New(sha256.New, []byte(c.apiKey))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
