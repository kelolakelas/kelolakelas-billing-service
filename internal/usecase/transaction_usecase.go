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
	txRepo             repository.TransactionRepository
	walletRepo         repository.WalletRepository
	ledgerRepo         repository.LedgerEntryRepository
	subscriptionRepo   repository.SubscriptionRepository
	paymentGateway     domain.PaymentGateway
	academicClient     academic.Client
	cfg                config.Config
	txManager          repository.BillingTransactionManager
	reconciliationRepo repository.PaymentReconciliationRepository
}

func NewTransactionUsecase(
	txRepo repository.TransactionRepository,
	walletRepo repository.WalletRepository,
	ledgerRepo repository.LedgerEntryRepository,
	subscriptionRepo repository.SubscriptionRepository,
	paymentGateway domain.PaymentGateway,
	academicClient academic.Client,
	cfg config.Config,
	txManagers ...repository.BillingTransactionManager,
) TransactionUsecase {
	var txManager repository.BillingTransactionManager
	if len(txManagers) > 0 {
		txManager = txManagers[0]
	}
	return newTransactionUsecase(txRepo, walletRepo, ledgerRepo, subscriptionRepo, paymentGateway, academicClient, cfg, txManager, nil)
}

func NewTransactionUsecaseWithReconciliation(
	txRepo repository.TransactionRepository,
	walletRepo repository.WalletRepository,
	ledgerRepo repository.LedgerEntryRepository,
	subscriptionRepo repository.SubscriptionRepository,
	paymentGateway domain.PaymentGateway,
	academicClient academic.Client,
	cfg config.Config,
	txManager repository.BillingTransactionManager,
	reconciliationRepo repository.PaymentReconciliationRepository,
) TransactionUsecase {
	return newTransactionUsecase(txRepo, walletRepo, ledgerRepo, subscriptionRepo, paymentGateway, academicClient, cfg, txManager, reconciliationRepo)
}

func newTransactionUsecase(
	txRepo repository.TransactionRepository,
	walletRepo repository.WalletRepository,
	ledgerRepo repository.LedgerEntryRepository,
	subscriptionRepo repository.SubscriptionRepository,
	paymentGateway domain.PaymentGateway,
	academicClient academic.Client,
	cfg config.Config,
	txManager repository.BillingTransactionManager,
	reconciliationRepo repository.PaymentReconciliationRepository,
) TransactionUsecase {
	return &transactionUsecase{
		txRepo:           txRepo,
		walletRepo:       walletRepo,
		ledgerRepo:       ledgerRepo,
		subscriptionRepo: subscriptionRepo,
		paymentGateway:   paymentGateway,
		academicClient:   academicClient, cfg: cfg, txManager: txManager, reconciliationRepo: reconciliationRepo,
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
		TenantID:        req.TenantID,
		ParentID:        req.ParentID,
		StudentID:       req.StudentID,
		BillingCycle:    req.BillingCycle,
		NextBillingDate: nextBillingDate,
		Status:          "pending",
		BillingEmail:    req.SenderEmail,
		ParentName:      req.SenderName,
		ClassName:       title,
		Amount:          grossAmount,
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
		BillingEmail:           req.SenderEmail,
	}
	initialPeriod := time.Now()
	tx.BillingPeriodStart = &initialPeriod

	if existing, err := u.txRepo.GetByEnrollmentID(ctx, req.EnrollmentID); err == nil {
		tx = existing
		tx.SubscriptionID = &subscription.ID
	} else if err := u.txRepo.Create(ctx, tx); err != nil {
		if existing, getErr := u.txRepo.GetByEnrollmentID(ctx, req.EnrollmentID); getErr == nil {
			tx = existing
		} else {
			return nil, fmt.Errorf("failed to create transaction record: %w", err)
		}
	}
	if lockingRepo, ok := u.txRepo.(repository.TransactionLockingRepository); ok {
		claimed, claimErr := lockingRepo.ClaimInvoice(ctx, tx.ID)
		if claimErr != nil {
			return nil, fmt.Errorf("failed to claim invoice creation: %w", claimErr)
		}
		if !claimed {
			current, getErr := u.txRepo.GetByEnrollmentID(ctx, req.EnrollmentID)
			if getErr != nil {
				return nil, fmt.Errorf("failed to read invoice state: %w", getErr)
			}
			if current.CheckoutSessionURL != nil && *current.CheckoutSessionURL != "" {
				return &domain.GenerateSubscriptionPaymentResponse{TransactionID: current.ID, CheckoutSessionURL: *current.CheckoutSessionURL, PaymentIntentID: valueOrEmpty(current.PaymentIntentID), GrossAmount: current.GrossAmount, Status: current.Status}, nil
			}
			return nil, fmt.Errorf("invoice creation is already in progress")
		}
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
		ExpiryPeriod:    0,
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
	response := &domain.TransactionResponse{ID: tx.ID, MerchantOrderID: tx.MerchantOrderID, TenantID: tx.TenantID, ParentID: tx.ParentID, StudentID: tx.StudentID, EnrollmentID: tx.EnrollmentID, VoucherID: tx.VoucherID, SubtotalAmount: tx.SubtotalAmount, DiscountAmount: tx.DiscountAmount, GrossAmount: tx.GrossAmount, PlatformFee: tx.PlatformFee, PaymentGatewayFee: tx.PaymentGatewayFee, NetAmount: tx.NetAmount, Currency: tx.Currency, Status: tx.Status, PaymentGatewayProvider: provider, PaymentIntentID: intent, CheckoutSessionURL: checkout, PaidAt: tx.PaidAt, CreatedAt: tx.CreatedAt, UpdatedAt: tx.UpdatedAt}
	if tx.Reconciliation != nil {
		status := tx.Reconciliation.Status
		if status == domain.ReconciliationStatusPending || status == domain.ReconciliationStatusProcessing {
			status = "reconciling"
		}
		response.ReconciliationStatus = status
		response.ReconciliationAttempts = tx.Reconciliation.AttemptCount
		response.ReconciliationNextAttemptAt = tx.Reconciliation.NextAttemptAt
		if tx.Reconciliation.LastError != nil {
			response.ReconciliationLastError = *tx.Reconciliation.LastError
		}
	}
	return response
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
	var tx *domain.Transaction
	if u.txManager != nil {
		err := u.txManager.WithTransaction(ctx, func(txCtx context.Context) error {
			return u.handleDuitkuWebhookLocal(txCtx, payload, &tx)
		})
		if err != nil {
			return err
		}
	} else if err := u.handleDuitkuWebhookLocal(ctx, payload, &tx); err != nil {
		return err
	}
	if tx == nil || tx.Status != "paid" {
		return nil
	}
	return u.reconcilePayment(ctx, tx)
}

func (u *transactionUsecase) ensureReconciliation(ctx context.Context, tx *domain.Transaction) error {
	if u.reconciliationRepo == nil {
		return nil
	}
	now := time.Now()
	return u.reconciliationRepo.Ensure(ctx, &domain.PaymentReconciliation{
		ID:            uuid.New(),
		TransactionID: tx.ID,
		EnrollmentID:  tx.EnrollmentID,
		Status:        domain.ReconciliationStatusPending,
		NextAttemptAt: &now,
	})
}

func (u *transactionUsecase) reconcilePayment(ctx context.Context, tx *domain.Transaction) error {
	if u.reconciliationRepo == nil {
		if u.academicClient == nil {
			return nil
		}
		return u.academicClient.ActivateEnrollment(ctx, tx.EnrollmentID)
	}
	reconciliation, err := u.reconciliationRepo.ClaimDue(ctx, tx.ID, time.Now(), reconciliationLease)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to claim enrollment reconciliation: %w", err)
	}
	return processClaimedReconciliation(ctx, u.reconciliationRepo, u.academicClient, u.cfg, reconciliation, time.Now())
}

func (u *transactionUsecase) handleDuitkuWebhookLocal(ctx context.Context, payload *domain.DuitkuCallbackPayload, result **domain.Transaction) error {
	transactionID, err := uuid.Parse(payload.MerchantOrderID)
	var tx *domain.Transaction
	if err != nil {
		if lookup, ok := u.txRepo.(interface {
			GetByMerchantOrderID(context.Context, string) (*domain.Transaction, error)
		}); ok {
			tx, err = lookup.GetByMerchantOrderID(ctx, payload.MerchantOrderID)
		} else {
			return domain.ErrTransactionNotFound
		}
	}
	if lockingRepo, ok := u.txRepo.(repository.TransactionLockingRepository); ok {
		if tx != nil {
			tx, err = lockingRepo.GetByIDForUpdate(ctx, tx.ID)
		} else {
			tx, err = lockingRepo.GetByIDForUpdate(ctx, transactionID)
		}
	} else {
		if tx == nil {
			tx, err = u.txRepo.GetByID(ctx, transactionID)
		}
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrTransactionNotFound
		}
		return fmt.Errorf("failed to fetch transaction: %w", err)
	}

	// A paid transaction still needs activation reconciliation on callback replay.
	if tx.Status == "paid" {
		if err := u.ensureReconciliation(ctx, tx); err != nil {
			return fmt.Errorf("failed to enqueue enrollment reconciliation: %w", err)
		}
		if result != nil {
			*result = tx
		}
		return nil
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
				period := now
				if tx.BillingPeriodStart != nil {
					period = *tx.BillingPeriodStart
				}
				nextDate, dateErr := nextBillingDate(period, subscription.BillingCycle)
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
		if !tx.IsSandbox {

			// 6. Credit Tenant Wallet
			var wallet *domain.Wallet
			if lockingRepo, ok := u.walletRepo.(repository.WalletLockingRepository); ok {
				wallet, err = lockingRepo.GetByTenantIDForUpdate(ctx, tx.TenantID)
			} else {
				wallet, err = u.walletRepo.GetByTenantID(ctx, tx.TenantID)
			}
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
		}
		if err := u.ensureReconciliation(ctx, tx); err != nil {
			return fmt.Errorf("failed to enqueue enrollment reconciliation: %w", err)
		}

		if result != nil {
			*result = tx
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
