package domain

import "context"

type CreateInvoiceRequest struct {
	MerchantOrderID string
	Amount          int64
	ProductDetails  string
	Email           string
	PhoneNumber     string
	CustomerVAName  string
	PaymentMethod   string
	CallbackURL     string
	ReturnURL       string
	ExpiryPeriod    int
}

type CreateInvoiceResponse struct {
	Reference  string
	PaymentURL string
}

type DuitkuCallbackPayload struct {
	MerchantCode    string `form:"merchantCode" json:"merchantCode" binding:"required"`
	Amount          string `form:"amount" json:"amount" binding:"required"`
	MerchantOrderID string `form:"merchantOrderId" json:"merchantOrderId" binding:"required"`
	ProductDetail   string `form:"productDetail" json:"productDetail"`
	PaymentCode     string `form:"paymentCode" json:"paymentCode"`
	ResultCode      string `form:"resultCode" json:"resultCode" binding:"required"`
	MerchantUserID  string `form:"merchantUserId" json:"merchantUserId"`
	Reference       string `form:"reference" json:"reference"`
	Signature       string `form:"signature" json:"signature" binding:"required"`
	SettlementDate  string `form:"settlementDate" json:"settlementDate"`
}

type PaymentGateway interface {
	CreateInvoice(ctx context.Context, request *CreateInvoiceRequest) (*CreateInvoiceResponse, error)
	ValidateCallbackSignature(payload *DuitkuCallbackPayload) bool
}

type EmailMessage struct {
	To, Subject, HTML string
}

type EmailClient interface {
	Send(ctx context.Context, message EmailMessage) error
}
