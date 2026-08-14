package usecase

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/academic"
)

type transactionUsecase struct {
	txRepo           repository.TransactionRepository
	walletRepo       repository.WalletRepository
	ledgerRepo       repository.LedgerEntryRepository
	subscriptionRepo repository.SubscriptionRepository
	paymentGateway   domain.PaymentGateway
	academicClient   academic.Client
	cfg              config.Config
}

func NewTransactionUsecase(
	txRepo repository.TransactionRepository,
	walletRepo repository.WalletRepository,
	ledgerRepo repository.LedgerEntryRepository,
	subscriptionRepo repository.SubscriptionRepository,
	paymentGateway domain.PaymentGateway,
	academicClient academic.Client,
	cfg config.Config,
) TransactionUsecase {
	return &transactionUsecase{
		txRepo:           txRepo,
		walletRepo:       walletRepo,
		ledgerRepo:       ledgerRepo,
		subscriptionRepo: subscriptionRepo,
		paymentGateway:   paymentGateway,
		academicClient:   academicClient,
		cfg:              cfg,
	}
}

func (u *transactionUsecase) CreateTransaction(ctx context.Context, tx *domain.Transaction) error {
	return u.txRepo.Create(ctx, tx)
}

func (u *transactionUsecase) GetTransaction(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	return u.txRepo.GetByID(ctx, id)
}

func (u *transactionUsecase) GenerateSubscriptionPayment(ctx context.Context, req *domain.GenerateSubscriptionPaymentRequest) (*domain.GenerateSubscriptionPaymentResponse, error) {
	if existing, err := u.txRepo.GetByEnrollmentID(ctx, req.EnrollmentID); err == nil {
		if existing.CheckoutSessionURL != nil && *existing.CheckoutSessionURL != "" {
			return &domain.GenerateSubscriptionPaymentResponse{TransactionID: existing.ID, CheckoutSessionURL: *existing.CheckoutSessionURL, PaymentIntentID: valueOrEmpty(existing.PaymentIntentID), GrossAmount: existing.GrossAmount, Status: existing.Status}, nil
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("failed to find existing enrollment payment: %w", err)
	}
	grossAmount := req.SubtotalAmount - req.DiscountAmount
	if grossAmount <= 0 {
		return nil, fmt.Errorf("gross_amount must be greater than 0")
	}

	netAmount := grossAmount - req.PlatformFee - req.PaymentGatewayFee
	if netAmount < 0 {
		return nil, fmt.Errorf("fees cannot exceed gross_amount")
	}

	title := req.Title
	if title == "" {
		title = fmt.Sprintf("Class Subscription Enrollment %s", req.EnrollmentID.String())
	}

	nextBillingDate, err := nextBillingDate(time.Now(), req.BillingCycle)
	if err != nil {
		return nil, err
	}
	subscription := &domain.Subscription{
		ID:              uuid.New(),
		EnrollmentID:    req.EnrollmentID,
		BillingCycle:    req.BillingCycle,
		NextBillingDate: nextBillingDate,
		Status:          "pending",
	}
	if existing, err := u.subscriptionRepo.GetByEnrollmentID(ctx, req.EnrollmentID); err == nil {
		subscription = existing
	} else if err := u.subscriptionRepo.Create(ctx, subscription); err != nil {
		return nil, fmt.Errorf("failed to create subscription: %w", err)
	}

	provider := "duitku"
	transactionID := req.EnrollmentID
	tx := &domain.Transaction{
		ID:                     transactionID,
		MerchantOrderID:        transactionID.String(),
		TenantID:               req.TenantID,
		ParentID:               req.ParentID,
		StudentID:              req.StudentID,
		EnrollmentID:           req.EnrollmentID,
		VoucherID:              req.VoucherID,
		SubtotalAmount:         req.SubtotalAmount,
		DiscountAmount:         req.DiscountAmount,
		GrossAmount:            grossAmount,
		PlatformFee:            req.PlatformFee,
		PaymentGatewayFee:      req.PaymentGatewayFee,
		NetAmount:              netAmount,
		SubscriptionID:         &subscription.ID,
		Currency:               "IDR",
		Status:                 "pending",
		IsSandbox:              strings.Contains(strings.ToLower(u.cfg.DuitkuAPIBaseURL), "sandbox"),
		PaymentGatewayProvider: &provider,
	}

	if existing, err := u.txRepo.GetByEnrollmentID(ctx, req.EnrollmentID); err == nil {
		tx = existing
		tx.SubscriptionID = &subscription.ID
	} else if err := u.txRepo.Create(ctx, tx); err != nil {
		return nil, fmt.Errorf("failed to create transaction record: %w", err)
	}

	invoice, err := u.paymentGateway.CreateInvoice(ctx, &domain.CreateInvoiceRequest{
		MerchantOrderID: transactionID.String(),
		Amount:          grossAmount,
		ProductDetails:  title,
		Email:           req.SenderEmail,
		PhoneNumber:     req.SenderPhone,
		CustomerVAName:  req.SenderName,
		PaymentMethod:   "VC",
		CallbackURL:     u.cfg.DuitkuCallbackURL,
		ReturnURL:       u.cfg.DuitkuReturnURL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate Duitku payment link: %w", err)
	}
	tx.PaymentIntentID = &invoice.Reference
	tx.CheckoutSessionURL = &invoice.PaymentURL
	if err := u.txRepo.Update(ctx, tx); err != nil {
		return nil, fmt.Errorf("failed to save Duitku payment details: %w", err)
	}

	return &domain.GenerateSubscriptionPaymentResponse{
		TransactionID:      tx.ID,
		CheckoutSessionURL: invoice.PaymentURL,
		PaymentIntentID:    invoice.Reference,
		GrossAmount:        grossAmount,
		Status:             tx.Status,
	}, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func transactionResponse(tx *domain.Transaction) *domain.TransactionResponse {
	provider := ""
	if tx.PaymentGatewayProvider != nil {
		provider = *tx.PaymentGatewayProvider
	}
	intent := ""
	if tx.PaymentIntentID != nil {
		intent = *tx.PaymentIntentID
	}
	checkout := ""
	if tx.CheckoutSessionURL != nil {
		checkout = *tx.CheckoutSessionURL
	}
	return &domain.TransactionResponse{ID: tx.ID, MerchantOrderID: tx.MerchantOrderID, TenantID: tx.TenantID, ParentID: tx.ParentID, StudentID: tx.StudentID, EnrollmentID: tx.EnrollmentID, VoucherID: tx.VoucherID, SubtotalAmount: tx.SubtotalAmount, DiscountAmount: tx.DiscountAmount, GrossAmount: tx.GrossAmount, PlatformFee: tx.PlatformFee, PaymentGatewayFee: tx.PaymentGatewayFee, NetAmount: tx.NetAmount, Currency: tx.Currency, Status: tx.Status, PaymentGatewayProvider: provider, PaymentIntentID: intent, CheckoutSessionURL: checkout, PaidAt: tx.PaidAt, CreatedAt: tx.CreatedAt, UpdatedAt: tx.UpdatedAt}
}
func (u *transactionUsecase) List(ctx context.Context, tenantID, parentID *uuid.UUID, query domain.TransactionQuery) (*domain.TransactionListResponse, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 100 {
		query.PageSize = 20
	}
	items, total, err := u.txRepo.List(ctx, tenantID, parentID, query)
	if err != nil {
		return nil, err
	}
	out := make([]domain.TransactionResponse, 0, len(items))
	for i := range items {
		out = append(out, *transactionResponse(&items[i]))
	}
	return &domain.TransactionListResponse{Items: out, Pagination: struct {
		Page       int   `json:"page"`
		PageSize   int   `json:"page_size"`
		TotalItems int64 `json:"total_items"`
		TotalPages int   `json:"total_pages"`
	}{query.Page, query.PageSize, total, int(math.Ceil(float64(total) / float64(query.PageSize)))}}, nil
}
func (u *transactionUsecase) GetByIDScoped(ctx context.Context, tenantID, parentID *uuid.UUID, id uuid.UUID) (*domain.TransactionResponse, error) {
	tx, err := u.txRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if tenantID != nil && tx.TenantID != *tenantID {
		return nil, gorm.ErrRecordNotFound
	}
	if parentID != nil && tx.ParentID != *parentID {
		return nil, gorm.ErrRecordNotFound
	}
	return transactionResponse(tx), nil
}

func (u *transactionUsecase) HandleDuitkuWebhook(ctx context.Context, payload *domain.DuitkuCallbackPayload) error {
	transactionID, err := uuid.Parse(payload.MerchantOrderID)
	if err != nil {
		return domain.ErrTransactionNotFound
	}
	tx, err := u.txRepo.GetByID(ctx, transactionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrTransactionNotFound
		}
		return fmt.Errorf("failed to fetch transaction: %w", err)
	}

	// A paid transaction still needs activation reconciliation on callback replay.
	if tx.Status == "paid" {
		return u.academicClient.ActivateEnrollment(ctx, tx.EnrollmentID)
	}

	amount, err := strconv.ParseInt(payload.Amount, 10, 64)
	if err != nil || amount != tx.GrossAmount {
		return fmt.Errorf("callback amount does not match transaction")
	}

	if payload.ResultCode == "00" {
		now := time.Now()
		tx.Status = "paid"
		tx.PaidAt = &now
		if payload.PaymentCode != "" {
			tx.PaymentMethod = &payload.PaymentCode
		}
		if payload.Reference != "" {
			tx.PaymentIntentID = &payload.Reference
		}

		if err := u.txRepo.Update(ctx, tx); err != nil {
			return fmt.Errorf("failed to update transaction status: %w", err)
		}

		if tx.SubscriptionID != nil {
			subscription, getErr := u.subscriptionRepo.GetByEnrollmentID(ctx, tx.EnrollmentID)
			if getErr != nil && !errors.Is(getErr, gorm.ErrRecordNotFound) {
				return fmt.Errorf("failed to fetch subscription: %w", getErr)
			}
			if subscription != nil {
				subscription.Status = "active"
				nextDate, dateErr := nextBillingDate(now, subscription.BillingCycle)
				if dateErr != nil {
					return dateErr
				}
				subscription.NextBillingDate = nextDate
				if err := u.subscriptionRepo.Update(ctx, subscription); err != nil {
					return fmt.Errorf("failed to update subscription: %w", err)
				}
			}
		}

		// Sandbox callbacks must never create real tenant balance or ledger entries.
		if tx.IsSandbox {
			return u.academicClient.ActivateEnrollment(ctx, tx.EnrollmentID)
		}

		// 6. Credit Tenant Wallet
		wallet, err := u.walletRepo.GetByTenantID(ctx, tx.TenantID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				wallet = &domain.Wallet{
					ID:               uuid.New(),
					TenantID:         tx.TenantID,
					AvailableBalance: tx.NetAmount,
					PendingBalance:   0,
				}
				if createErr := u.walletRepo.Create(ctx, wallet); createErr != nil {
					return fmt.Errorf("failed to create tenant wallet: %w", createErr)
				}
			} else {
				return fmt.Errorf("failed to fetch tenant wallet: %w", err)
			}
		} else {
			wallet.AvailableBalance += tx.NetAmount
			if updateErr := u.walletRepo.Update(ctx, wallet); updateErr != nil {
				return fmt.Errorf("failed to update tenant wallet balance: %w", updateErr)
			}
		}

		// Record Ledger Entry
		desc := fmt.Sprintf("Subscription payment received for enrollment %s", tx.EnrollmentID)
		ledgerEntry := &domain.LedgerEntry{
			ID:            uuid.New(),
			WalletID:      wallet.ID,
			ReferenceID:   tx.ID,
			ReferenceType: "transaction",
			Amount:        tx.NetAmount,
			EntryType:     "payment_received",
			Description:   &desc,
		}
		if err := u.ledgerRepo.Create(ctx, ledgerEntry); err != nil {
			return fmt.Errorf("failed to create ledger entry: %w", err)
		}

		// 7. Trigger status update for Enrollments record to 'active' in academic-service
		if err := u.academicClient.ActivateEnrollment(ctx, tx.EnrollmentID); err != nil {
			return fmt.Errorf("failed to update enrollment status in academic service: %w", err)
		}
	} else if payload.ResultCode == "01" || payload.ResultCode == "02" {
		tx.Status = "failed"
		if err := u.txRepo.Update(ctx, tx); err != nil {
			return fmt.Errorf("failed to update transaction status to failed: %w", err)
		}
	}

	return nil
}

func nextBillingDate(from time.Time, cycle string) (time.Time, error) {
	switch cycle {
	case domain.BillingCycleMonthly:
		return from.AddDate(0, 1, 0), nil
	case domain.BillingCycleQuarterly:
		return from.AddDate(0, 3, 0), nil
	case domain.BillingCycleYearly:
		return from.AddDate(1, 0, 0), nil
	default:
		return time.Time{}, fmt.Errorf("unsupported billing cycle %q", cycle)
	}
}
