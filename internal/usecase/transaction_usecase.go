package usecase

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/requestid"
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
	outcomeEmail       domain.EmailClient
	// feePolicy reads identity's applied platform fee policy (KEL-99). A nil
	// reader refuses every new transaction instead of charging 0%.
	feePolicy domain.PlatformFeePolicyReader
	vouchers  repository.VoucherReservationRepository
}

// WithPlatformFeePolicy attaches the platform fee policy reader used for every
// new transaction. Without it GenerateSubscriptionPayment fails closed.
func WithPlatformFeePolicy(usecase TransactionUsecase, reader domain.PlatformFeePolicyReader) TransactionUsecase {
	if concrete, ok := usecase.(*transactionUsecase); ok {
		concrete.feePolicy = reader
	}
	return usecase
}

// newTransactionFees reads the applied platform fee policy and computes the fee
// snapshot for a transaction that does not exist yet. It never falls back to a
// 0% fee: a missing reader or any read error is ErrPlatformFeePolicyUnavailable.
func newTransactionFees(ctx context.Context, reader domain.PlatformFeePolicyReader, gross, gatewayFee int64) (domain.PlatformFeeBreakdown, error) {
	if reader == nil {
		return domain.PlatformFeeBreakdown{}, domain.ErrPlatformFeePolicyUnavailable
	}
	policy, err := reader.AppliedPlatformFeePolicy(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrPlatformFeePolicyUnavailable) {
			return domain.PlatformFeeBreakdown{}, err
		}
		return domain.PlatformFeeBreakdown{}, fmt.Errorf("%w: %v", domain.ErrPlatformFeePolicyUnavailable, err)
	}
	if err := policy.Validate(); err != nil {
		return domain.PlatformFeeBreakdown{}, err
	}
	return domain.ComputePlatformFee(policy, gross, gatewayFee)
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
	outcomeEmail ...domain.EmailClient,
) TransactionUsecase {
	usecase := newTransactionUsecase(txRepo, walletRepo, ledgerRepo, subscriptionRepo, paymentGateway, academicClient, cfg, txManager, reconciliationRepo)
	if len(outcomeEmail) > 0 {
		usecase.(*transactionUsecase).outcomeEmail = outcomeEmail[0]
	}
	return usecase
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
	// Never accept client-selected voucher ids or amounts, even from internal callers.
	copyReq := *req
	req = &copyReq
	req.VoucherID = nil
	req.DiscountAmount = 0
	if u.vouchers != nil && !req.PrivateScheduleRequest {
		if err := u.prepareVoucherCheckout(ctx, req); err != nil {
			return nil, err
		}
	} else if strings.TrimSpace(req.VoucherCode) != "" {
		return nil, domain.ErrVoucherRejected
	}
	// Validate at the trusted boundary too: internal callers can bypass HTTP binding.
	paymentMethod := req.PaymentMethod
	if paymentMethod == "" {
		paymentMethod = "VC"
	}
	switch paymentMethod {
	case "VC", "VA", "BC", "SP", "NQ":
	default:
		return nil, domain.ErrInvalidPaymentMethod
	}
	now := time.Now().UTC()
	hasTransaction := false
	if existing, err := u.txRepo.GetByEnrollmentID(ctx, req.EnrollmentID); err == nil {
		if response, ok := reusableInvoiceResponse(existing, now); ok {
			u.dispatchPrivatePaymentEmail(ctx, existing.ID)
			return response, nil
		}
		hasTransaction = true
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("failed to find existing enrollment payment: %w", err)
	}
	grossAmount := req.SubtotalAmount - req.DiscountAmount
	if grossAmount <= 0 {
		return nil, fmt.Errorf("gross_amount must be greater than 0")
	}

	// KEL-99: the platform fee comes from identity's applied policy, never from
	// req.PlatformFee. It is decided before anything is written, so a policy
	// outage or a fee above the gross amount leaves no subscription or
	// transaction behind. An enrollment that already has a transaction keeps the
	// snapshot it was created with; its invoice is reissued unchanged.
	var fees domain.PlatformFeeBreakdown
	if !hasTransaction {
		var err error
		if fees, err = newTransactionFees(ctx, u.feePolicy, grossAmount, req.PaymentGatewayFee); err != nil {
			return nil, err
		}
	}

	title := req.Title
	if title == "" {
		title = fmt.Sprintf("Class Subscription Enrollment %s", req.EnrollmentID.String())
	}

	nextBillingDate, err := nextBillingDate(now, req.BillingCycle)
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
		PaymentGatewayFee:      req.PaymentGatewayFee,
		SubscriptionID:         &subscription.ID,
		Currency:               "IDR",
		Status:                 domain.TransactionStatusPending,
		IsSandbox:              strings.Contains(strings.ToLower(u.cfg.DuitkuAPIBaseURL), "sandbox"),
		PaymentGatewayProvider: &provider,
		BillingEmail:           req.SenderEmail,
		PrivateScheduleRequest: req.PrivateScheduleRequest,
		ClassName:              subscription.ClassName,
		PaymentMethod:          &paymentMethod,
	}
	tx.BillingPeriodStart = &now
	fees.Apply(tx)

	existing, err := u.txRepo.GetByEnrollmentID(ctx, req.EnrollmentID)
	ownsInvoice := false
	switch {
	case err == nil:
		// The enrollment already has a transaction: reuse the row and its merchant
		// order id so the parent keeps a single payment record, then take ownership of
		// issuing a replacement invoice. ClaimReinvoice only matches unpaid rows, so a
		// transaction that was paid in the meantime is never re-invoiced.
		switch existing.Status {
		case domain.TransactionStatusPaid:
			return nil, domain.ErrTransactionAlreadyPaid
		case "cancelled", "refunded":
			return nil, domain.ErrInvalidTransactionStatus
		}
		tx = existing
		// Reissues keep the transaction's original channel; a replay cannot switch
		// a previously issued payment to another provider method.
		if tx.PaymentMethod != nil && *tx.PaymentMethod != "" {
			paymentMethod = *tx.PaymentMethod
		} else {
			paymentMethod = "VC" // legacy invoices were always issued as VC
		}
		tx.SubscriptionID = &subscription.ID
		claimed, claimErr := u.claimReinvoice(ctx, tx.ID, now)
		if claimErr != nil {
			return nil, claimErr
		}
		if !claimed {
			if response, ok := reusableInvoiceResponse(existing, now); ok {
				return response, nil
			}
			return nil, fmt.Errorf("invoice creation is already in progress")
		}
		ownsInvoice = true
	case errors.Is(err, gorm.ErrRecordNotFound):
		if hasTransaction {
			// The row seen above vanished before this read, so no fee was decided
			// yet; decide it now rather than inserting a transaction without one.
			if fees, err = newTransactionFees(ctx, u.feePolicy, grossAmount, req.PaymentGatewayFee); err != nil {
				return nil, err
			}
			fees.Apply(tx)
		}
		if createErr := u.txRepo.Create(ctx, tx); createErr != nil {
			if current, getErr := u.txRepo.GetByEnrollmentID(ctx, req.EnrollmentID); getErr == nil {
				tx = current
			} else {
				return nil, fmt.Errorf("failed to create transaction record: %w", createErr)
			}
		}
	default:
		return nil, fmt.Errorf("failed to find existing enrollment payment: %w", err)
	}
	if !ownsInvoice {
		if lockingRepo, ok := u.txRepo.(repository.TransactionLockingRepository); ok {
			claimed, claimErr := lockingRepo.ClaimInvoice(ctx, tx.ID, now, u.claimTimeoutMinutes())
			if claimErr != nil {
				return nil, fmt.Errorf("failed to claim invoice creation: %w", claimErr)
			}
			if !claimed {
				current, getErr := u.txRepo.GetByEnrollmentID(ctx, req.EnrollmentID)
				if getErr != nil {
					return nil, fmt.Errorf("failed to read invoice state: %w", getErr)
				}
				if response, ok := reusableInvoiceResponse(current, now); ok {
					return response, nil
				}
				return nil, fmt.Errorf("invoice creation is already in progress")
			}
		}
	}

	// The winner's persisted transaction selects the method, including after a
	// concurrent insert lost the unique enrollment constraint.
	if tx.PaymentMethod != nil && *tx.PaymentMethod != "" {
		paymentMethod = *tx.PaymentMethod
	} else {
		paymentMethod = "VC"
	}
	grossAmount = tx.GrossAmount // Reissues use the persisted snapshot, never replay input.
	validityMinutes := u.invoiceValidityMinutes()
	invoice, err := u.paymentGateway.CreateInvoice(ctx, &domain.CreateInvoiceRequest{
		MerchantOrderID: tx.MerchantOrderID,
		Amount:          grossAmount,
		ProductDetails:  title,
		Email:           req.SenderEmail,
		PhoneNumber:     req.SenderPhone,
		CustomerVAName:  req.SenderName,
		PaymentMethod:   paymentMethod,
		CallbackURL:     u.cfg.DuitkuCallbackURL,
		ReturnURL:       u.cfg.DuitkuReturnURL,
		ExpiryPeriod:    validityMinutes,
	})
	if err != nil {
		// The claim is released on the same error path that took it, so a transient
		// provider failure can never leave the enrollment holding a `creating` row that
		// no later request is allowed to claim. The release is a conditional update: it
		// only matches the row this call still owns, so a response that arrived late, or
		// a cancellation that won the race, is never overwritten.
		u.releaseFailedInvoiceClaim(ctx, tx.ID, err, now)
		return nil, fmt.Errorf("failed to generate Duitku payment link: %w", err)
	}
	// The replacement invoice makes the transaction payable again, so it returns to
	// `pending` with a fresh expiry that mirrors the validity requested above. The
	// write is conditional: a cancellation that happened while the provider invoice
	// was being created leaves the row in `cancelled`, and this statement then matches
	// nothing. The payment link is discarded in that case, and the seat release job the
	// cancellation enqueued still gives the seat back.
	expiresAt := domain.InvoiceExpiresAt(now, validityMinutes)
	issued, err := u.markInvoiceIssued(ctx, tx, invoice, expiresAt, now)
	if err != nil {
		return nil, err
	}
	if !issued {
		if err := u.cancelPendingSeatRelease(ctx, tx); err != nil {
			return nil, err
		}
		return nil, domain.ErrInvalidTransactionStatus
	}
	tx.Status = domain.TransactionStatusPending
	tx.ExpiredAt = nil
	tx.InvoiceExpiresAt = &expiresAt
	tx.PaymentIntentID = &invoice.Reference
	tx.CheckoutSessionURL = &invoice.PaymentURL
	setPaymentInstructions(tx, invoice)
	tx.UpdatedAt = now

	// The transaction is payable again, so a seat release that has not been claimed
	// yet is withdrawn and must never drop this seat. The withdrawal runs after the
	// status change on purpose: if it fails, the transaction is recoverable by
	// re-requesting the invoice, whereas cancelling first could strand a seat with no
	// job left to release it. A release that already ran is left alone, because the
	// seat is genuinely gone and the paid callback must surface that rejection.
	if err := u.cancelPendingSeatRelease(ctx, tx); err != nil {
		return nil, err
	}

	u.dispatchPrivatePaymentEmail(ctx, tx.ID)
	return &domain.GenerateSubscriptionPaymentResponse{
		TransactionID:      tx.ID,
		CheckoutSessionURL: invoice.PaymentURL,
		PaymentIntentID:    invoice.Reference,
		GrossAmount:        grossAmount,
		Status:             tx.Status,
	}, nil
}

// markInvoiceIssued stores the payment link created for a transaction and reports
// whether the row still expected one. Repositories without the conditional update
// fall back to the previous unconditional save, which is correct for the single
// caller that owns the row through ClaimInvoice or ClaimReinvoice.
func (u *transactionUsecase) markInvoiceIssued(ctx context.Context, tx *domain.Transaction, invoice *domain.CreateInvoiceResponse, expiresAt, now time.Time) (bool, error) {
	if invoice == nil {
		return false, fmt.Errorf("invoice response is missing")
	}
	if lockingRepo, ok := u.txRepo.(repository.TransactionLockingRepository); ok {
		issued, err := lockingRepo.MarkInvoiceIssued(ctx, tx.ID, invoice, expiresAt)
		if err != nil {
			return false, fmt.Errorf("failed to save Duitku payment details: %w", err)
		}
		return issued, nil
	}
	tx.Status = domain.TransactionStatusPending
	tx.ExpiredAt = nil
	tx.InvoiceExpiresAt = &expiresAt
	tx.PaymentIntentID = &invoice.Reference
	tx.CheckoutSessionURL = &invoice.PaymentURL
	setPaymentInstructions(tx, invoice)
	tx.InvoiceClaimedAt = nil
	tx.InvoiceFailureReason = nil
	tx.PrivatePaymentEmailClaimedAt = nil
	tx.PrivatePaymentEmailSentAt = nil
	tx.PrivatePaymentEmailFailureReason = nil
	tx.UpdatedAt = now
	if err := u.txRepo.Update(ctx, tx); err != nil {
		return false, fmt.Errorf("failed to save Duitku payment details: %w", err)
	}
	return true, nil
}

func setPaymentInstructions(tx *domain.Transaction, invoice *domain.CreateInvoiceResponse) {
	// Empty provider fields replace old instructions rather than inheriting them.
	tx.VANumber = instructionPointer(invoice.VANumber)
	tx.QRString = instructionPointer(invoice.QRString)
	tx.AppURL = instructionPointer(invoice.AppURL)
}

func instructionPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// releaseFailedInvoiceClaim hands a refused or failed invoice creation back as a
// claimable transaction and records why it failed, so the enrollment is never left
// holding a `creating` row that no later request is allowed to claim.
//
// It is best effort on purpose: the caller is already returning the provider error,
// and a failure to write the release must not replace that error with a less useful
// one. The claim timeout covers this case, so a release that could not be written
// still stops being a dead end once the claim ages out.
func (u *transactionUsecase) releaseFailedInvoiceClaim(ctx context.Context, id uuid.UUID, cause error, now time.Time) {
	releaseFailedInvoiceClaim(ctx, u.txRepo, id, cause, now)
}

// releaseFailedInvoiceClaim is the shared recovery used by every invoice-creation
// path (direct payment requests and the subscription renewal worker) so a failed
// attempt always leaves the transaction in the same recoverable state.
//
// The release is a conditional update that only matches the `creating` row the caller
// still owns. When it matches nothing the transaction was already moved on — the
// payment link was stored after all, or the parent cancelled while the provider was
// being called — and that newer state must be preserved rather than overwritten.
func releaseFailedInvoiceClaim(ctx context.Context, repo repository.TransactionRepository, id uuid.UUID, cause error, now time.Time) {
	lockingRepo, ok := repo.(repository.TransactionLockingRepository)
	if !ok {
		return
	}
	reason := "invoice creation failed"
	if cause != nil && cause.Error() != "" {
		reason = cause.Error()
	}
	restored, err := lockingRepo.RestoreFailedInvoiceClaim(ctx, id, reason, now)
	if err != nil {
		slog.ErrorContext(ctx, "failed to release invoice claim", "transaction_id", id.String(), "error", err)
		return
	}
	if !restored {
		slog.WarnContext(ctx, "invoice claim was not released because the transaction is no longer owned",
			"transaction_id", id.String())
	}
}

// reusableInvoiceResponse returns the existing payment link when the transaction
// still has an invoice the parent can actually pay, so repeated requests for the
// same enrollment stay idempotent instead of creating another invoice. A link
// whose stored validity has already passed is never returned.
func reusableInvoiceResponse(tx *domain.Transaction, now time.Time) (*domain.GenerateSubscriptionPaymentResponse, bool) {
	if tx == nil || tx.CheckoutSessionURL == nil || *tx.CheckoutSessionURL == "" {
		return nil, false
	}
	switch tx.Status {
	case domain.TransactionStatusPending, domain.TransactionStatusCreating:
	default:
		return nil, false
	}
	if tx.InvoiceExpiresAt != nil && !tx.InvoiceExpiresAt.After(now) {
		return nil, false
	}
	return &domain.GenerateSubscriptionPaymentResponse{
		TransactionID:      tx.ID,
		CheckoutSessionURL: *tx.CheckoutSessionURL,
		PaymentIntentID:    valueOrEmpty(tx.PaymentIntentID),
		GrossAmount:        tx.GrossAmount,
		Status:             tx.Status,
	}, true
}

// claimReinvoice takes ownership of creating a replacement invoice for the given
// transaction. Repositories that cannot do the atomic claim fall back to the
// upstream invoice-creation guard.
func (u *transactionUsecase) claimReinvoice(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	lockingRepo, ok := u.txRepo.(repository.TransactionLockingRepository)
	if !ok {
		return true, nil
	}
	claimed, err := lockingRepo.ClaimReinvoice(ctx, id, now, u.claimTimeoutMinutes())
	if err != nil {
		return false, fmt.Errorf("failed to claim replacement invoice: %w", err)
	}
	return claimed, nil
}

// claimTimeoutMinutes is how long a `creating` row may stay unowned before another
// request is allowed to take it over. A stored value of zero means the deployment
// never configured one, so the domain default applies.
func (u *transactionUsecase) claimTimeoutMinutes() int {
	if u.cfg.TransactionClaimTimeoutMinutes <= 0 {
		return domain.DefaultTransactionClaimTimeoutMinutes
	}
	return u.cfg.TransactionClaimTimeoutMinutes
}

// invoiceValidityMinutes is the payment window requested from the payment gateway
// and stored as the local invoice expiry. Both the first invoice and renewal
// invoices use the configured subscription payment period so a single setting
// describes how long an unpaid invoice stays payable.
func (u *transactionUsecase) invoiceValidityMinutes() int {
	days := u.cfg.SubscriptionPaymentExpiryPeriodDays
	if days <= 0 {
		days = 14
	}
	return days * 24 * 60
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
	payable := tx.Status == domain.TransactionStatusPending && (tx.InvoiceExpiresAt == nil || tx.InvoiceExpiresAt.After(time.Now().UTC()))
	if payable && tx.PaymentIntentID != nil {
		intent = *tx.PaymentIntentID
	}
	checkout := ""
	if payable && tx.CheckoutSessionURL != nil {
		checkout = *tx.CheckoutSessionURL
	}
	response := &domain.TransactionResponse{ID: tx.ID, MerchantOrderID: tx.MerchantOrderID, TenantID: tx.TenantID, ParentID: tx.ParentID, StudentID: tx.StudentID, EnrollmentID: tx.EnrollmentID, VoucherID: tx.VoucherID, SubtotalAmount: tx.SubtotalAmount, DiscountAmount: tx.DiscountAmount, GrossAmount: tx.GrossAmount, PlatformFee: tx.PlatformFee, PaymentGatewayFee: tx.PaymentGatewayFee, NetAmount: tx.NetAmount, PlatformFeePolicyVersion: tx.PlatformFeePolicyVersion, PlatformFeePercentBps: tx.PlatformFeePercentBps, PlatformFeeFixed: tx.PlatformFeeFixed, Currency: tx.Currency, Status: tx.Status, PaymentGatewayProvider: provider, PaymentIntentID: intent, CheckoutSessionURL: checkout, InvoiceExpiresAt: tx.InvoiceExpiresAt, ExpiredAt: tx.ExpiredAt, PaidAt: tx.PaidAt, CreatedAt: tx.CreatedAt, UpdatedAt: tx.UpdatedAt}
	if payable && checkout != "" {
		response.PaymentMethod = valueOrEmpty(tx.PaymentMethod)
		response.VANumber = valueOrEmpty(tx.VANumber)
		response.QRString = valueOrEmpty(tx.QRString)
		response.AppURL = valueOrEmpty(tx.AppURL)
	}
	if tx.Reconciliation != nil {
		status := tx.Reconciliation.Status
		if status == domain.ReconciliationStatusPending || status == domain.ReconciliationStatusProcessing {
			status = "reconciling"
		}
		response.ReconciliationStatus = status
		// The kind tells an operator whether the outstanding work confirms the seat
		// (activation) or gives it back (release), which is what distinguishes a
		// payment still settling from a failed payment that freed the seat again.
		response.ReconciliationKind = tx.Reconciliation.Kind
		response.ReconciliationAttempts = tx.Reconciliation.AttemptCount
		response.ReconciliationNextAttemptAt = tx.Reconciliation.NextAttemptAt
		if tx.Reconciliation.LastError != nil {
			response.ReconciliationLastError = *tx.Reconciliation.LastError
		}
	}
	return response
}

// CancelEnrollmentPayment withdraws the unpaid invoice of an enrollment that the
// parent cancelled. The transition is deliberately asymmetric and idempotent:
//
//   - an unpaid transaction becomes `cancelled` and keeps a seat release job, so the
//     provider invoice may still be payable but can never be turned into an
//     activated seat without a paid callback being recorded;
//   - a transaction that already settled is never rewritten. `refunded` and `paid`
//     answer ErrInvalidTransactionStatus so the caller refuses the cancellation
//     instead of dropping a seat that was paid for;
//   - repeating the request for an already cancelled transaction is a success, so
//     academic can safely retry after a failed attempt;
//   - an enrollment with no transaction at all (invoice creation never completed) is
//     still cancellable, because there is nothing to pay and the seat must be freed.
func (u *transactionUsecase) CancelEnrollmentPayment(ctx context.Context, enrollmentID uuid.UUID) (*domain.TransactionResponse, error) {
	if enrollmentID == uuid.Nil {
		return nil, domain.ErrTransactionNotFound
	}
	tx, err := u.txRepo.GetByEnrollmentID(ctx, enrollmentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrTransactionNotFound
		}
		return nil, fmt.Errorf("failed to find enrollment payment: %w", err)
	}

	switch tx.Status {
	case domain.TransactionStatusCancelled:
		return transactionResponse(tx), nil
	case domain.TransactionStatusPaid, domain.TransactionStatusRefunded:
		return nil, domain.ErrInvalidTransactionStatus
	}

	cancelled, err := u.cancelUnpaidTransaction(ctx, tx.ID)
	if err != nil {
		return nil, err
	}
	if !cancelled {
		// The row changed between the read and the conditional update. Re-read it so
		// the caller is answered from the state that actually won: a payment that
		// settled in the meantime must be reported as a refusal, not as a success.
		current, getErr := u.txRepo.GetByID(ctx, tx.ID)
		if getErr != nil {
			return nil, fmt.Errorf("failed to read cancellation result: %w", getErr)
		}
		if current.Status == domain.TransactionStatusCancelled {
			return transactionResponse(current), nil
		}
		return nil, domain.ErrInvalidTransactionStatus
	}

	current, err := u.txRepo.GetByID(ctx, tx.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to read cancelled transaction: %w", err)
	}
	return transactionResponse(current), nil
}

// cancelUnpaidTransaction performs the conditional update. Repositories that cannot
// do it atomically fall back to the in-memory transition so a repository without the
// locking capability still behaves consistently. The fallback enqueues the seat
// release itself, because the atomic statement owns that job and a repository that
// only transitions the status must not silently leave the seat held.
func (u *transactionUsecase) cancelUnpaidTransaction(ctx context.Context, id uuid.UUID) (bool, error) {
	if lockingRepo, ok := u.txRepo.(repository.TransactionLockingRepository); ok {
		cancelled, err := lockingRepo.CancelUnpaid(ctx, id)
		if err != nil {
			return false, fmt.Errorf("failed to cancel unpaid transaction: %w", err)
		}
		return cancelled, nil
	}
	tx, err := u.txRepo.GetByID(ctx, id)
	if err != nil {
		return false, fmt.Errorf("failed to find transaction: %w", err)
	}
	tx.Status = domain.TransactionStatusCancelled
	if err := u.txRepo.Update(ctx, tx); err != nil {
		return false, fmt.Errorf("failed to cancel unpaid transaction: %w", err)
	}
	if err := u.enqueueSeatRelease(ctx, tx); err != nil {
		return false, fmt.Errorf("failed to enqueue enrollment release: %w", err)
	}
	return true, nil
}

func (u *transactionUsecase) SalesSummary(ctx context.Context, tenantID uuid.UUID, from, until time.Time) ([]domain.SalesSummary, error) {
	repo, ok := u.txRepo.(repository.SalesSummaryRepository)
	if !ok {
		return nil, fmt.Errorf("sales summary repository unavailable")
	}
	return repo.SummarizePaid(ctx, tenantID, from, until)
}

// transactionExportBatchSize bounds how many rows one CSV batch holds in
// memory. Small enough to stay flat, large enough to avoid a query per row.
const transactionExportBatchSize = 500

func (u *transactionUsecase) ExportTransactions(ctx context.Context, tenantID uuid.UUID, query domain.TransactionQuery, w io.Writer) (int64, error) {
	repo, ok := u.txRepo.(repository.TransactionExportRepository)
	if !ok {
		return 0, fmt.Errorf("transaction export repository unavailable")
	}
	writer := domain.NewTransactionExportWriter(w)
	if err := domain.WriteTransactionExportHeader(writer); err != nil {
		return 0, fmt.Errorf("failed to write export header: %w", err)
	}
	var rows int64
	err := repo.IterateExport(ctx, tenantID, query, transactionExportBatchSize, func(batch []domain.Transaction) error {
		for i := range batch {
			if err := domain.WriteTransactionExportRow(writer, batch[i]); err != nil {
				return fmt.Errorf("failed to write export row: %w", err)
			}
			rows++
		}
		// Flush per batch so the client starts receiving rows while the
		// range is still being read, and memory stays bounded by the batch.
		writer.Flush()
		return writer.Error()
	})
	if err != nil {
		return rows, err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return rows, fmt.Errorf("failed to flush export: %w", err)
	}
	return rows, nil
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
	var confirmed *domain.PaymentStatus
	if payload.ResultCode == domain.ResultCodeSuccess {
		// Provider I/O must not hold the transaction row lock. The locked path below
		// rechecks the row and its amount before committing any financial effect.
		id, err := uuid.Parse(payload.MerchantOrderID)
		var existing *domain.Transaction
		if err == nil {
			existing, err = u.txRepo.GetByID(ctx, id)
		} else if lookup, ok := u.txRepo.(interface {
			GetByMerchantOrderID(context.Context, string) (*domain.Transaction, error)
		}); ok {
			existing, err = lookup.GetByMerchantOrderID(ctx, payload.MerchantOrderID)
		} else {
			return domain.ErrTransactionNotFound
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrTransactionNotFound
		}
		if err != nil {
			return fmt.Errorf("failed to fetch transaction: %w", err)
		}
		if existing.Status != domain.TransactionStatusPaid {
			amount, parseErr := strconv.ParseInt(payload.Amount, 10, 64)
			if parseErr != nil || amount != existing.GrossAmount {
				return fmt.Errorf("callback amount does not match transaction")
			}
			confirmed, err = u.paymentGateway.TransactionStatus(ctx, payload.MerchantOrderID)
			if err != nil {
				slog.WarnContext(ctx, "Duitku payment status unavailable", "merchant_order_id", payload.MerchantOrderID, "transaction_id", existing.ID.String(), "error", err)
				return fmt.Errorf("failed to confirm Duitku payment status: %w", err)
			}
			if confirmed == nil {
				return fmt.Errorf("empty Duitku payment status")
			}
		}
	}
	var tx *domain.Transaction
	if u.txManager != nil {
		err := u.txManager.WithTransaction(ctx, func(txCtx context.Context) error {
			return u.handleDuitkuWebhookLocal(txCtx, payload, confirmed, &tx)
		})
		if err != nil {
			return err
		}
	} else if err := u.handleDuitkuWebhookLocal(ctx, payload, confirmed, &tx); err != nil {
		return err
	}
	if tx == nil {
		return nil
	}
	u.sendOutcomeEmail(ctx, tx)
	if tx.Status != domain.TransactionStatusPaid {
		return nil
	}
	return u.reconcilePayment(ctx, tx)
}

func (u *transactionUsecase) sendOutcomeEmail(ctx context.Context, tx *domain.Transaction) {
	if u.outcomeEmail == nil || tx == nil || tx.BillingEmail == "" {
		return
	}
	var subject, body string
	className := tx.ClassName
	if className == "" {
		className = "kelas Anda"
	}
	class := html.EscapeString(className)
	transactionID := html.EscapeString(tx.MerchantOrderID)
	switch tx.Status {
	case domain.TransactionStatusPaid:
		subject = "Pembayaran berhasil"
		body = fmt.Sprintf("<html><body><p>Halo, pembayaran untuk kelas <strong>%s</strong> telah kami terima.</p><p>Jumlah: IDR %d</p><p>ID transaksi: %s</p></body></html>", class, tx.GrossAmount, transactionID)
	case domain.TransactionStatusFailed:
		subject = "Pembayaran belum berhasil"
		continuePayment := "Buka kembali halaman tagihan di KelolaKelas untuk memilih metode pembayaran dan melanjutkan pembayaran."
		if link := valueOrEmpty(tx.CheckoutSessionURL); link != "" {
			continuePayment = fmt.Sprintf("Silakan lanjutkan pembayaran melalui tautan berikut: <a href=\"%s\">Lanjutkan pembayaran</a>.", html.EscapeString(link))
		}
		body = fmt.Sprintf("<html><body><p>Pembayaran untuk kelas <strong>%s</strong> sebesar IDR %d belum berhasil.</p><p>%s</p><p>ID transaksi: %s</p></body></html>", class, tx.GrossAmount, continuePayment, transactionID)
	default:
		return
	}
	lockingRepo, ok := u.txRepo.(repository.TransactionLockingRepository)
	if !ok {
		return
	}
	sentAt := time.Now().UTC().Round(time.Microsecond)
	claimed, err := lockingRepo.ClaimOutcomeEmail(ctx, tx.ID, tx.Status, sentAt)
	if err != nil || !claimed {
		if err != nil {
			slog.WarnContext(ctx, "failed to claim payment outcome email", "transaction_id", tx.ID.String(), "error", err)
		}
		return
	}
	if err := u.outcomeEmail.Send(ctx, domain.EmailMessage{To: tx.BillingEmail, Subject: subject, HTML: body}); err != nil {
		slog.WarnContext(ctx, "failed to send payment outcome email", "transaction_id", tx.ID.String(), "error", err)
		if _, releaseErr := lockingRepo.ReleaseOutcomeEmailClaim(ctx, tx.ID, tx.Status, sentAt); releaseErr != nil {
			slog.ErrorContext(ctx, "failed to release payment outcome email claim", "transaction_id", tx.ID.String(), "error", releaseErr)
		}
	}
}

func (u *transactionUsecase) ensureReconciliation(ctx context.Context, tx *domain.Transaction) error {
	// Renewal lifecycle is handled by the subscription job; activation belongs
	// only to the initial purchase. An expired renewal must not release its seat.
	if tx.SubscriptionID != nil && tx.BillingPeriodStart != nil {
		return nil
	}
	if u.reconciliationRepo == nil {
		return nil
	}
	now := time.Now().UTC()
	return u.reconciliationRepo.EnsureActivation(ctx, &domain.PaymentReconciliation{
		ID:            uuid.New(),
		TransactionID: tx.ID,
		EnrollmentID:  tx.EnrollmentID,
		Kind:          domain.ReconciliationKindActivation,
		Status:        domain.ReconciliationStatusPending,
		NextAttemptAt: &now,
	})
}

// enqueueSeatRelease records that a failed or expired transaction must give its
// Academic seat back. It is called only after the terminal status is committed, so
// a rollback never leaves a release job for a transaction that is still payable.
// Non-enrollment transactions (billing-only records with no Academic enrollment)
// owe nothing and are skipped.
func (u *transactionUsecase) enqueueSeatRelease(ctx context.Context, tx *domain.Transaction) error {
	if u.reconciliationRepo == nil || tx == nil || tx.EnrollmentID == uuid.Nil || (tx.SubscriptionID != nil && tx.BillingPeriodStart != nil) {
		return nil
	}
	now := time.Now().UTC()
	return u.reconciliationRepo.EnqueueRelease(ctx, &domain.PaymentReconciliation{
		ID:            uuid.New(),
		TransactionID: tx.ID,
		EnrollmentID:  tx.EnrollmentID,
		Kind:          domain.ReconciliationKindRelease,
		Status:        domain.ReconciliationStatusPending,
		NextAttemptAt: &now,
	})
}

// cancelPendingSeatRelease withdraws a seat release that has not been attempted yet,
// so issuing a replacement invoice cannot race the worker into dropping a seat the
// parent is about to pay for. A release that is already processing or finished is
// deliberately untouched: those seats are gone, and the failure must stay visible.
func (u *transactionUsecase) cancelPendingSeatRelease(ctx context.Context, tx *domain.Transaction) error {
	if u.reconciliationRepo == nil || tx == nil || tx.EnrollmentID == uuid.Nil {
		return nil
	}
	if err := u.reconciliationRepo.CancelPendingRelease(ctx, tx.ID); err != nil {
		return fmt.Errorf("failed to withdraw pending enrollment release: %w", err)
	}
	return nil
}

func (u *transactionUsecase) reconcilePayment(ctx context.Context, tx *domain.Transaction) error {
	if tx.SubscriptionID != nil && tx.BillingPeriodStart != nil {
		return nil
	}
	if u.reconciliationRepo == nil {
		if u.academicClient == nil {
			return nil
		}
		if err := u.academicClient.ActivateEnrollment(ctx, tx.EnrollmentID); err != nil {
			slog.WarnContext(ctx, "academic enrollment activation failed", "request_id", requestid.FromContext(ctx))
			return err
		}
		return nil
	}
	reconciliation, err := u.reconciliationRepo.ClaimDue(ctx, tx.ID, time.Now().UTC(), reconciliationLease)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to claim enrollment reconciliation: %w", err)
	}
	return processClaimedReconciliation(ctx, u.reconciliationRepo, u.academicClient, u.cfg, reconciliation, time.Now().UTC())
}

func (u *transactionUsecase) handleDuitkuWebhookLocal(ctx context.Context, payload *domain.DuitkuCallbackPayload, confirmed *domain.PaymentStatus, result **domain.Transaction) error {
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

	// A refunded payment is final: provider callback replay must not credit the
	// wallet again or recreate activation/subscription work.
	if tx.Status == domain.TransactionStatusRefunded {
		return nil
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

	switch payload.ResultCode {
	case domain.ResultCodeSuccess:
		statusCode := ""
		if confirmed != nil {
			statusCode = confirmed.StatusCode
		}
		if confirmed == nil || confirmed.StatusCode != domain.ResultCodeSuccess ||
			confirmed.MerchantOrderID != tx.MerchantOrderID || confirmed.Reference == "" || confirmed.Amount != tx.GrossAmount ||
			(tx.PaymentIntentID != nil && *tx.PaymentIntentID != "" && confirmed.Reference != *tx.PaymentIntentID) ||
			(payload.Reference != "" && confirmed.Reference != payload.Reference) {
			slog.WarnContext(ctx, "Duitku callback payment not confirmed",
				"merchant_order_id", tx.MerchantOrderID, "transaction_id", tx.ID.String(),
				"status_code", statusCode)
			return fmt.Errorf("Duitku payment status does not confirm transaction")
		}
		// A successful payment is honoured even when the local expiry already fired:
		// the parent really did pay, so the transaction becomes `paid` and the usual
		// activation reconciliation is enqueued. `expired_at` is kept as evidence that
		// the local deadline had passed while the payment was still settling.
		now := time.Now().UTC()
		tx.Status = domain.TransactionStatusPaid
		tx.PaidAt = &now
		if payload.PaymentCode != "" {
			if tx.PaymentMethod == nil || *tx.PaymentMethod == "" {
				tx.PaymentMethod = &payload.PaymentCode
			}
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
			if subscription != nil && subscription.Status != "cancelled" {
				if tx.BillingPeriodStart != nil && subscription.Status == "suspended" {
					lifecycle, ok := u.subscriptionRepo.(repository.SubscriptionLifecycleRepository)
					if !ok {
						return fmt.Errorf("subscription lifecycle repository unavailable")
					}
					if err := lifecycle.ResumePaid(ctx, subscription.ID, subscription.EnrollmentID, now); err != nil {
						return fmt.Errorf("failed to enqueue enrollment resume: %w", err)
					}
				}
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
	case domain.ResultCodeFailed, domain.ResultCodeCanceled:
		// Failure codes only ever move an unpaid transaction that is still awaiting a
		// decision to `failed`. A terminal state is never rewritten: a transaction
		// that was paid in the meantime keeps `paid`, and one the local expiry already
		// closed stays `expired` instead of losing that evidence. The seat is only
		// released for a transaction this branch actually moved, and the release job is
		// written through the same context so it commits or rolls back with the status
		// change — a released seat can never outlive a failed status write.
		if tx.Status == domain.TransactionStatusPending || tx.Status == domain.TransactionStatusCreating {
			tx.Status = domain.TransactionStatusFailed
			if err := u.txRepo.Update(ctx, tx); err != nil {
				return fmt.Errorf("failed to update transaction status to failed: %w", err)
			}
			if err := u.enqueueSeatRelease(ctx, tx); err != nil {
				return fmt.Errorf("failed to enqueue enrollment release: %w", err)
			}
		}
		// A failed callback replay is eligible to retry a notification whose send
		// previously failed and released its claim. Do not repeat the state transition.
		if tx.Status == domain.TransactionStatusFailed && result != nil {
			*result = tx
		}
	default:
		// Provider result codes outside the documented contract (00 success, 01
		// failed, 02 canceled) are not a financial outcome we can act on, so the
		// transaction is left untouched. The callback is still recorded in the logs
		// for investigation; see _docs/duitku/api.md for the provider contract.
		slog.WarnContext(ctx, "ignoring Duitku callback with unknown result code",
			"merchant_order_id", tx.MerchantOrderID,
			"transaction_id", tx.ID.String(),
			"enrollment_id", tx.EnrollmentID.String(),
			"result_code", payload.ResultCode,
			"payment_code", payload.PaymentCode,
			"reference", payload.Reference,
			"transaction_status", tx.Status,
		)
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
