package usecase

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/academic"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/flip"
)

type transactionUsecase struct {
	txRepo           repository.TransactionRepository
	walletRepo       repository.WalletRepository
	ledgerRepo       repository.LedgerEntryRepository
	subscriptionRepo repository.SubscriptionRepository
	flipClient       flip.Client
	academicClient   academic.Client
	cfg              config.Config
}

func NewTransactionUsecase(
	txRepo repository.TransactionRepository,
	walletRepo repository.WalletRepository,
	ledgerRepo repository.LedgerEntryRepository,
	subscriptionRepo repository.SubscriptionRepository,
	flipClient flip.Client,
	academicClient academic.Client,
	cfg config.Config,
) TransactionUsecase {
	return &transactionUsecase{
		txRepo:           txRepo,
		walletRepo:       walletRepo,
		ledgerRepo:       ledgerRepo,
		subscriptionRepo: subscriptionRepo,
		flipClient:       flipClient,
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

	// 1. Call Flip API to create PWF bill link
	flipReq := &flip.CreateBillRequest{
		Title:       title,
		Type:        "SINGLE",
		Amount:      &grossAmount,
		Step:        "checkout",
		ReferenceID: req.EnrollmentID.String(),
		SenderName:  req.SenderName,
		SenderEmail: req.SenderEmail,
		SenderPhone: req.SenderPhone,
	}

	flipResp, err := u.flipClient.CreateBill(ctx, flipReq)
	if err != nil {
		return nil, fmt.Errorf("failed to generate flip payment link: %w", err)
	}

	provider := "flip"
	intentID := flipResp.LinkID
	checkoutURL := flipResp.LinkURL
	if intentID == "" || checkoutURL == "" {
		return nil, fmt.Errorf("flip response did not contain bill link data")
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
		Status:          "active",
	}
	if err := u.subscriptionRepo.Create(ctx, subscription); err != nil {
		return nil, fmt.Errorf("failed to create subscription: %w", err)
	}

	// 2. Create Transaction record in DB
	tx := &domain.Transaction{
		ID:                     uuid.New(),
		TenantID:               req.TenantID,
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
		IsSandbox:              u.cfg.FlipBaseURL != "" && strings.Contains(u.cfg.FlipBaseURL, "sandbox"),
		PaymentGatewayProvider: &provider,
		PaymentIntentID:        &intentID,
		CheckoutSessionURL:     &checkoutURL,
	}

	if err := u.txRepo.Create(ctx, tx); err != nil {
		return nil, fmt.Errorf("failed to create transaction record: %w", err)
	}

	return &domain.GenerateSubscriptionPaymentResponse{
		TransactionID:      tx.ID,
		CheckoutSessionURL: checkoutURL,
		PaymentIntentID:    intentID,
		GrossAmount:        grossAmount,
		Status:             tx.Status,
	}, nil
}

func (u *transactionUsecase) HandleFlipWebhook(ctx context.Context, payload *domain.FlipWebhookPayload, validationToken string) error {
	// 1. Validate signature / token against FLIP_VALIDATION_TOKEN
	expectedToken := u.cfg.FlipValidationToken
	tokenToValidate := validationToken
	if tokenToValidate == "" {
		tokenToValidate = payload.Token
	}

	if expectedToken == "" || subtle.ConstantTimeCompare([]byte(tokenToValidate), []byte(expectedToken)) != 1 {
		return domain.ErrInvalidWebhookToken
	}

	// 2. Parse bill data
	billData, err := payload.ParseBillData()
	if err != nil {
		return fmt.Errorf("invalid webhook payload data: %w", err)
	}

	// 3. Find transaction by PaymentIntentID
	intentID := billData.BillLinkID
	if intentID == "" {
		intentID = billData.ID
	}

	tx, err := u.txRepo.GetByPaymentIntentID(ctx, intentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrTransactionNotFound
		}
		return fmt.Errorf("failed to fetch transaction: %w", err)
	}

	// 4. Idempotency check: if already paid, return early
	if tx.Status == "paid" {
		return nil
	}

	// 5. If status is SUCCESS or PAID
	if billData.Status == "SUCCESS" || billData.Status == "PAID" {
		now := time.Now()
		tx.Status = "paid"
		tx.PaidAt = &now
		if billData.SenderBank != "" {
			tx.PaymentMethod = &billData.SenderBank
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

		// Sandbox callbacks are useful for transaction state tests but must never
		// create real tenant balance or ledger entries.
		if tx.IsSandbox {
			return u.academicClient.UpdateEnrollmentStatus(ctx, tx.EnrollmentID, "active")
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
		if err := u.academicClient.UpdateEnrollmentStatus(ctx, tx.EnrollmentID, "active"); err != nil {
			return fmt.Errorf("failed to update enrollment status in academic service: %w", err)
		}
	} else if billData.Status == "FAILED" || billData.Status == "EXPIRED" {
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
