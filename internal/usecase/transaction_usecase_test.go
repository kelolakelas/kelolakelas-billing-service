package usecase

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/academic"
)

// transactionRepoStub is an in-memory stand-in for the transaction repository.
// Setting missing=true makes every lookup report gorm.ErrRecordNotFound so the
// first-invoice path can be exercised.
type transactionRepoStub struct {
	transaction      *domain.Transaction
	missing          bool
	createCount      int
	updateCount      int
	claimCount       int
	cancelUnpaidRuns int
}

func (r *transactionRepoStub) Create(_ context.Context, transaction *domain.Transaction) error {
	r.createCount++
	r.transaction = transaction
	r.missing = false
	return nil
}

func (r *transactionRepoStub) GetByID(context.Context, uuid.UUID) (*domain.Transaction, error) {
	return r.current()
}

func (r *transactionRepoStub) GetByMerchantOrderID(context.Context, string) (*domain.Transaction, error) {
	return r.current()
}

func (r *transactionRepoStub) GetByEnrollmentID(context.Context, uuid.UUID) (*domain.Transaction, error) {
	return r.current()
}

func (r *transactionRepoStub) GetBySubscriptionPeriod(context.Context, uuid.UUID, time.Time) (*domain.Transaction, error) {
	return nil, gorm.ErrRecordNotFound
}

func (r *transactionRepoStub) GetByPaymentIntentID(context.Context, string) (*domain.Transaction, error) {
	return nil, gorm.ErrRecordNotFound
}

func (r *transactionRepoStub) List(context.Context, *uuid.UUID, *uuid.UUID, domain.TransactionQuery) ([]domain.Transaction, int64, error) {
	return nil, 0, nil
}

func (r *transactionRepoStub) Update(context.Context, *domain.Transaction) error {
	r.updateCount++
	return nil
}

func (r *transactionRepoStub) GetByIDForUpdate(context.Context, uuid.UUID) (*domain.Transaction, error) {
	return r.current()
}

func (r *transactionRepoStub) ClaimInvoice(context.Context, uuid.UUID) (bool, error) {
	r.claimCount++
	return true, nil
}

// ClaimReinvoice mirrors the SQL guard of the real repository: only unpaid rows
// can be claimed for a replacement invoice, claiming moves the row to `creating`,
// and the stale gateway references are cleared so the next invoice starts clean.
func (r *transactionRepoStub) ClaimReinvoice(_ context.Context, _ uuid.UUID, _ time.Time) (bool, error) {
	r.claimCount++
	current, err := r.current()
	if err != nil {
		return false, nil
	}
	switch current.Status {
	case domain.TransactionStatusPaid, "cancelled", "refunded":
		return false, nil
	}
	current.Status = domain.TransactionStatusCreating
	current.CheckoutSessionURL = nil
	current.PaymentIntentID = nil
	return true, nil
}

func (r *transactionRepoStub) ClaimPaymentLinkEmail(context.Context, uuid.UUID, time.Time) (bool, error) {
	return false, nil
}

func (r *transactionRepoStub) ClaimReminderEmail(context.Context, uuid.UUID, time.Time, int) (bool, error) {
	return false, nil
}

// CancelUnpaid mirrors the SQL predicate of the real repository: only unpaid rows
// become cancelled, and a settled row is left untouched so the caller can refuse the
// cancellation instead of dropping a paid seat. Like the real statement it also
// records the seat release, so a usecase test that goes through this stub exercises
// the same side effect the database performs.
func (r *transactionRepoStub) CancelUnpaid(_ context.Context, id uuid.UUID) (bool, error) {
	r.cancelUnpaidRuns++
	current, err := r.current()
	if err != nil || current.ID != id {
		return false, nil
	}
	switch current.Status {
	case domain.TransactionStatusPending, domain.TransactionStatusCreating,
		domain.TransactionStatusFailed, domain.TransactionStatusExpired:
		current.Status = domain.TransactionStatusCancelled
		return true, nil
	}
	return false, nil
}

// MarkInvoiceIssued mirrors the conditional update of the real repository: the row
// only accepts a payment link while it is still expecting one, so a cancellation that
// landed first is not silently overwritten.
func (r *transactionRepoStub) MarkInvoiceIssued(_ context.Context, id uuid.UUID, checkoutSessionURL, paymentIntentID string, expiresAt time.Time) (bool, error) {
	current, err := r.current()
	if err != nil || current.ID != id {
		return false, nil
	}
	if current.Status != domain.TransactionStatusCreating && current.Status != domain.TransactionStatusPending {
		return false, nil
	}
	current.Status = domain.TransactionStatusPending
	current.ExpiredAt = nil
	current.InvoiceExpiresAt = &expiresAt
	current.CheckoutSessionURL = &checkoutSessionURL
	current.PaymentIntentID = &paymentIntentID
	return true, nil
}

func (r *transactionRepoStub) current() (*domain.Transaction, error) {
	if r.missing || r.transaction == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return r.transaction, nil
}

type walletRepoStub struct{ balance int64 }

func (w *walletRepoStub) GetByTenantID(context.Context, uuid.UUID) (*domain.Wallet, error) {
	return &domain.Wallet{ID: uuid.New(), AvailableBalance: w.balance}, nil
}
func (w *walletRepoStub) Create(context.Context, *domain.Wallet) error { return nil }
func (w *walletRepoStub) Update(_ context.Context, wallet *domain.Wallet) error {
	w.balance = wallet.AvailableBalance
	return nil
}

type ledgerRepoStub struct{ entries int }

func (l *ledgerRepoStub) Create(context.Context, *domain.LedgerEntry) error {
	l.entries++
	return nil
}
func (l *ledgerRepoStub) GetByID(context.Context, uuid.UUID) (*domain.LedgerEntry, error) {
	return nil, gorm.ErrRecordNotFound
}

type subscriptionRepoStub struct {
	subscription *domain.Subscription
	updates      int
}

func (s *subscriptionRepoStub) Create(context.Context, *domain.Subscription) error { return nil }
func (s *subscriptionRepoStub) GetByEnrollmentID(context.Context, uuid.UUID) (*domain.Subscription, error) {
	if s.subscription == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return s.subscription, nil
}
func (s *subscriptionRepoStub) Update(context.Context, *domain.Subscription) error {
	s.updates++
	return nil
}
func (s *subscriptionRepoStub) ListDueForRenewal(context.Context, time.Time) ([]domain.Subscription, error) {
	return nil, nil
}

type invoiceGatewayStub struct {
	requests []*domain.CreateInvoiceRequest
}

func (g *invoiceGatewayStub) CreateInvoice(_ context.Context, request *domain.CreateInvoiceRequest) (*domain.CreateInvoiceResponse, error) {
	g.requests = append(g.requests, request)
	return &domain.CreateInvoiceResponse{
		Reference:  "REF-" + uuid.NewString(),
		PaymentURL: "https://pay.example.com/" + uuid.NewString(),
	}, nil
}

func (g *invoiceGatewayStub) ValidateCallbackSignature(*domain.DuitkuCallbackPayload) bool {
	return true
}

func newTransactionUsecaseForTest(txRepo *transactionRepoStub, reconciliationRepo *reconciliationRepoStub, gateway domain.PaymentGateway, academicClient academic.Client, cfg config.Config) TransactionUsecase {
	return NewTransactionUsecaseWithReconciliation(
		txRepo, &walletRepoStub{}, &ledgerRepoStub{}, &subscriptionRepoStub{}, gateway, academicClient, cfg, nil, reconciliationRepo,
	)
}

func expiryTestConfig() config.Config {
	return config.Config{SubscriptionPaymentExpiryPeriodDays: 7, PaymentReconciliationMaxAttempts: 3}
}

func callbackPayload(tx *domain.Transaction, resultCode string) *domain.DuitkuCallbackPayload {
	return &domain.DuitkuCallbackPayload{
		MerchantCode:    "DXXXX",
		Amount:          strconv.FormatInt(tx.GrossAmount, 10),
		MerchantOrderID: tx.MerchantOrderID,
		PaymentCode:     "VC",
		ResultCode:      resultCode,
		Reference:       "REF-PAID",
		Signature:       "signature",
	}
}

// Acceptance criterion (b): a paid callback that arrives after the local expiry
// already fired must still settle the transaction and reconcile the enrollment.
func TestHandleDuitkuWebhookAcceptsPaidCallbackForExpiredTransaction(t *testing.T) {
	expiredAt := time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)
	subscriptionID := uuid.New()
	tx := &domain.Transaction{
		ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(),
		StudentID: uuid.New(), EnrollmentID: uuid.New(), GrossAmount: 40000, NetAmount: 38000,
		Currency: "IDR", Status: domain.TransactionStatusExpired, ExpiredAt: &expiredAt,
		IsSandbox: true, SubscriptionID: &subscriptionID,
	}
	repo := &transactionRepoStub{transaction: tx}
	subscriptionRepo := &subscriptionRepoStub{subscription: &domain.Subscription{
		ID: subscriptionID, EnrollmentID: tx.EnrollmentID, BillingCycle: domain.BillingCycleMonthly, Status: "pending",
	}}
	reconciliationRepo := &reconciliationRepoStub{item: &domain.PaymentReconciliation{
		ID: uuid.New(), TransactionID: tx.ID, EnrollmentID: tx.EnrollmentID, AttemptCount: 1,
	}}
	academicStub := &academicActivationStub{}
	usecase := NewTransactionUsecaseWithReconciliation(
		repo, &walletRepoStub{}, &ledgerRepoStub{}, subscriptionRepo, &invoiceGatewayStub{}, academicStub,
		config.Config{PaymentReconciliationMaxAttempts: 3}, nil, reconciliationRepo,
	)

	if err := usecase.HandleDuitkuWebhook(context.Background(), callbackPayload(tx, domain.ResultCodeSuccess)); err != nil {
		t.Fatalf("HandleDuitkuWebhook() error = %v", err)
	}
	if tx.Status != domain.TransactionStatusPaid {
		t.Fatalf("transaction status = %q, want %q", tx.Status, domain.TransactionStatusPaid)
	}
	if tx.PaidAt == nil {
		t.Fatal("transaction paid_at was not set")
	}
	if tx.ExpiredAt == nil || !tx.ExpiredAt.Equal(expiredAt) {
		t.Fatalf("local expiry evidence = %v, want %v", tx.ExpiredAt, expiredAt)
	}
	if academicStub.calls != 1 {
		t.Fatalf("ActivateEnrollment calls = %d, want 1", academicStub.calls)
	}
	if !reconciliationRepo.active {
		t.Fatal("enrollment reconciliation was not completed")
	}
	if reconciliationRepo.ensureCount != 1 {
		t.Fatalf("reconciliation Ensure calls = %d, want 1", reconciliationRepo.ensureCount)
	}
	if repo.createCount != 0 {
		t.Fatalf("transaction create count = %d, want 0 (a late payment must reuse the existing row)", repo.createCount)
	}
	if subscriptionRepo.subscription.Status != "active" {
		t.Fatalf("subscription status = %q, want active", subscriptionRepo.subscription.Status)
	}
}

// Acceptance criterion (c): an undocumented result code is logged and leaves the
// transaction untouched.
func TestHandleDuitkuWebhookIgnoresUnknownResultCode(t *testing.T) {
	tx := &domain.Transaction{
		ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(),
		StudentID: uuid.New(), EnrollmentID: uuid.New(), GrossAmount: 40000, NetAmount: 38000,
		Currency: "IDR", Status: domain.TransactionStatusPending, IsSandbox: true,
	}
	repo := &transactionRepoStub{transaction: tx}
	reconciliationRepo := &reconciliationRepoStub{item: &domain.PaymentReconciliation{ID: uuid.New(), EnrollmentID: tx.EnrollmentID, AttemptCount: 1}}
	usecase := newTransactionUsecaseForTest(repo, reconciliationRepo, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())

	if err := usecase.HandleDuitkuWebhook(context.Background(), callbackPayload(tx, "77")); err != nil {
		t.Fatalf("HandleDuitkuWebhook() error = %v", err)
	}
	if tx.Status != domain.TransactionStatusPending {
		t.Fatalf("transaction status = %q, want %q", tx.Status, domain.TransactionStatusPending)
	}
	if repo.updateCount != 0 {
		t.Fatalf("transaction update count = %d, want 0", repo.updateCount)
	}
	if tx.PaidAt != nil {
		t.Fatal("an unknown result code must not mark the transaction paid")
	}
	if reconciliationRepo.ensureCount != 0 || reconciliationRepo.claimCount != 0 {
		t.Fatalf("reconciliation was touched: ensure=%d claim=%d", reconciliationRepo.ensureCount, reconciliationRepo.claimCount)
	}
}

// Acceptance criterion (d): failure codes never rewrite a terminal state, so a
// paid or locally expired transaction keeps its financial outcome.
func TestHandleDuitkuWebhookDoesNotDowngradeSettledTransactions(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		code    string
		want    string
		updates int
	}{
		{name: "paid stays paid on cancel code", status: domain.TransactionStatusPaid, code: domain.ResultCodeCanceled, want: domain.TransactionStatusPaid},
		{name: "expired stays expired on failure code", status: domain.TransactionStatusExpired, code: domain.ResultCodeFailed, want: domain.TransactionStatusExpired},
		{name: "expired stays expired on cancel code", status: domain.TransactionStatusExpired, code: domain.ResultCodeCanceled, want: domain.TransactionStatusExpired},
		{name: "pending becomes failed on failure code", status: domain.TransactionStatusPending, code: domain.ResultCodeFailed, want: domain.TransactionStatusFailed, updates: 1},
		{name: "pending becomes failed on cancel code", status: domain.TransactionStatusPending, code: domain.ResultCodeCanceled, want: domain.TransactionStatusFailed, updates: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &domain.Transaction{
				ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(),
				StudentID: uuid.New(), EnrollmentID: uuid.New(), GrossAmount: 40000, NetAmount: 38000,
				Currency: "IDR", Status: test.status, IsSandbox: true,
			}
			repo := &transactionRepoStub{transaction: tx}
			reconciliationRepo := &reconciliationRepoStub{item: &domain.PaymentReconciliation{
				ID: uuid.New(), TransactionID: tx.ID, EnrollmentID: tx.EnrollmentID, AttemptCount: 1,
			}}
			usecase := newTransactionUsecaseForTest(repo, reconciliationRepo, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())

			if err := usecase.HandleDuitkuWebhook(context.Background(), callbackPayload(tx, test.code)); err != nil {
				t.Fatalf("HandleDuitkuWebhook() error = %v", err)
			}
			if tx.Status != test.want {
				t.Fatalf("transaction status = %q, want %q", tx.Status, test.want)
			}
			if repo.updateCount != test.updates {
				t.Fatalf("transaction update count = %d, want %d", repo.updateCount, test.updates)
			}
			if test.status == domain.TransactionStatusPaid && reconciliationRepo.ensureCount != 1 {
				t.Fatalf("reconciliation Ensure calls = %d, want 1 (a paid replay must re-enqueue activation)", reconciliationRepo.ensureCount)
			}
		})
	}
}

func TestHandleDuitkuWebhookRejectsAmountMismatch(t *testing.T) {
	tx := &domain.Transaction{
		ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(),
		StudentID: uuid.New(), EnrollmentID: uuid.New(), GrossAmount: 40000,
		Currency: "IDR", Status: domain.TransactionStatusExpired, IsSandbox: true,
	}
	repo := &transactionRepoStub{transaction: tx}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())
	payload := callbackPayload(tx, domain.ResultCodeSuccess)
	payload.Amount = "1"

	if err := usecase.HandleDuitkuWebhook(context.Background(), payload); err == nil {
		t.Fatal("HandleDuitkuWebhook() error = nil, want an amount mismatch error")
	}
	if tx.Status != domain.TransactionStatusExpired || repo.updateCount != 0 {
		t.Fatalf("transaction changed on a mismatched amount: status=%q updates=%d", tx.Status, repo.updateCount)
	}
}

func TestGenerateSubscriptionPaymentReusesInvoiceStillValid(t *testing.T) {
	link := "https://pay.example.com/still-valid"
	expiresAt := time.Now().Add(time.Hour)
	tx := &domain.Transaction{
		ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(),
		StudentID: uuid.New(), EnrollmentID: uuid.New(), GrossAmount: 40000, Currency: "IDR",
		Status: domain.TransactionStatusPending, CheckoutSessionURL: &link, InvoiceExpiresAt: &expiresAt,
		PaymentIntentID: strPtr("REF-EXISTING"),
	}
	repo := &transactionRepoStub{transaction: tx}
	gateway := &invoiceGatewayStub{}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, expiryTestConfig())

	response, err := usecase.GenerateSubscriptionPayment(context.Background(), &domain.GenerateSubscriptionPaymentRequest{
		EnrollmentID: tx.EnrollmentID, TenantID: tx.TenantID, ParentID: tx.ParentID,
		BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000,
	})
	if err != nil {
		t.Fatalf("GenerateSubscriptionPayment() error = %v", err)
	}
	if response.CheckoutSessionURL != link {
		t.Fatalf("checkout session URL = %q, want the existing link", response.CheckoutSessionURL)
	}
	if len(gateway.requests) != 0 {
		t.Fatalf("gateway calls = %d, want 0", len(gateway.requests))
	}
	if tx.Status != domain.TransactionStatusPending {
		t.Fatalf("transaction status = %q, want %q", tx.Status, domain.TransactionStatusPending)
	}
}

// Acceptance criterion: an expired transaction can be paid again through the
// existing idempotent flow, without creating a duplicate transaction row.
func TestGenerateSubscriptionPaymentReissuesExpiredInvoice(t *testing.T) {
	stale := "https://pay.example.com/stale"
	expiredAt := time.Now().Add(-time.Minute)
	tx := &domain.Transaction{
		ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(),
		StudentID: uuid.New(), EnrollmentID: uuid.New(), GrossAmount: 40000, NetAmount: 38000,
		Currency: "IDR", Status: domain.TransactionStatusExpired, CheckoutSessionURL: &stale,
		ExpiredAt: &expiredAt, PaymentIntentID: strPtr("REF-STALE"),
	}
	repo := &transactionRepoStub{transaction: tx}
	gateway := &invoiceGatewayStub{}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, expiryTestConfig())

	response, err := usecase.GenerateSubscriptionPayment(context.Background(), &domain.GenerateSubscriptionPaymentRequest{
		EnrollmentID: tx.EnrollmentID, TenantID: tx.TenantID, ParentID: tx.ParentID,
		BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000,
	})
	if err != nil {
		t.Fatalf("GenerateSubscriptionPayment() error = %v", err)
	}
	if response.CheckoutSessionURL == stale || response.CheckoutSessionURL == "" {
		t.Fatalf("checkout session URL = %q, want a replacement link", response.CheckoutSessionURL)
	}
	if tx.Status != domain.TransactionStatusPending {
		t.Fatalf("transaction status = %q, want %q", tx.Status, domain.TransactionStatusPending)
	}
	if tx.ExpiredAt != nil {
		t.Fatal("expired_at was not cleared when the transaction became payable again")
	}
	if tx.InvoiceExpiresAt == nil || !tx.InvoiceExpiresAt.After(time.Now()) {
		t.Fatalf("invoice expiry = %v, want a future deadline", tx.InvoiceExpiresAt)
	}
	if repo.createCount != 0 {
		t.Fatalf("transaction create count = %d, want 0 (the existing row must be reused)", repo.createCount)
	}
	if repo.claimCount != 1 {
		t.Fatalf("ClaimReinvoice calls = %d, want 1", repo.claimCount)
	}
	if len(gateway.requests) != 1 {
		t.Fatalf("gateway calls = %d, want 1", len(gateway.requests))
	}
	if gateway.requests[0].ExpiryPeriod != 7*24*60 {
		t.Fatalf("requested expiry period = %d, want %d", gateway.requests[0].ExpiryPeriod, 7*24*60)
	}
	if gateway.requests[0].MerchantOrderID != tx.MerchantOrderID {
		t.Fatalf("merchant order id = %q, want the original %q", gateway.requests[0].MerchantOrderID, tx.MerchantOrderID)
	}
}

func TestGenerateSubscriptionPaymentStoresExpiryForNewTransaction(t *testing.T) {
	repo := &transactionRepoStub{missing: true}
	gateway := &invoiceGatewayStub{}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, expiryTestConfig())

	response, err := usecase.GenerateSubscriptionPayment(context.Background(), &domain.GenerateSubscriptionPaymentRequest{
		EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(),
		BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000,
	})
	if err != nil {
		t.Fatalf("GenerateSubscriptionPayment() error = %v", err)
	}
	if response.CheckoutSessionURL == "" {
		t.Fatal("checkout session URL was not returned")
	}
	if repo.createCount != 1 {
		t.Fatalf("transaction create count = %d, want 1", repo.createCount)
	}
	if repo.transaction.InvoiceExpiresAt == nil {
		t.Fatal("invoice expiry was not stored on the new transaction")
	}
	if time.Until(*repo.transaction.InvoiceExpiresAt) < 6*24*time.Hour {
		t.Fatalf("invoice expiry = %v, want roughly the configured 7 days", repo.transaction.InvoiceExpiresAt)
	}
	if len(gateway.requests) != 1 || gateway.requests[0].ExpiryPeriod != 7*24*60 {
		t.Fatalf("gateway requests = %v", gateway.requests)
	}
}

func TestGenerateSubscriptionPaymentRejectsPaidTransaction(t *testing.T) {
	link := "https://pay.example.com/paid"
	tx := &domain.Transaction{
		ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(),
		StudentID: uuid.New(), EnrollmentID: uuid.New(), GrossAmount: 40000, Currency: "IDR",
		Status: domain.TransactionStatusPaid, CheckoutSessionURL: &link, PaidAt: timePtr(time.Now()),
	}
	repo := &transactionRepoStub{transaction: tx}
	gateway := &invoiceGatewayStub{}
	usecase := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, expiryTestConfig())

	_, err := usecase.GenerateSubscriptionPayment(context.Background(), &domain.GenerateSubscriptionPaymentRequest{
		EnrollmentID: tx.EnrollmentID, TenantID: tx.TenantID, ParentID: tx.ParentID,
		BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000,
	})
	if !errors.Is(err, domain.ErrTransactionAlreadyPaid) {
		t.Fatalf("GenerateSubscriptionPayment() error = %v, want ErrTransactionAlreadyPaid", err)
	}
	if len(gateway.requests) != 0 {
		t.Fatalf("gateway calls = %d, want 0", len(gateway.requests))
	}
	if repo.updateCount != 0 {
		t.Fatalf("transaction update count = %d, want 0", repo.updateCount)
	}
}

// Acceptance criterion (a): the parent-facing transaction payload exposes the
// invoice deadline and the local expiry timestamp.
func TestTransactionResponseExposesInvoiceExpiry(t *testing.T) {
	expiresAt := time.Now()
	expiredAt := time.Now().Add(-time.Hour)
	tx := &domain.Transaction{
		ID: uuid.New(), GrossAmount: 40000, Currency: "IDR", Status: domain.TransactionStatusExpired,
		InvoiceExpiresAt: &expiresAt, ExpiredAt: &expiredAt,
	}
	response := transactionResponse(tx)
	if response.Status != domain.TransactionStatusExpired {
		t.Fatalf("status = %q, want %q", response.Status, domain.TransactionStatusExpired)
	}
	if response.InvoiceExpiresAt == nil || !response.InvoiceExpiresAt.Equal(expiresAt) {
		t.Fatalf("invoice_expires_at = %v, want %v", response.InvoiceExpiresAt, expiresAt)
	}
	if response.ExpiredAt == nil || !response.ExpiredAt.Equal(expiredAt) {
		t.Fatalf("expired_at = %v, want %v", response.ExpiredAt, expiredAt)
	}
}

func TestReusableInvoiceResponseSkipsUnusableLinks(t *testing.T) {
	link := "https://pay.example.com/stale"
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Minute)
	tests := []struct {
		name string
		tx   *domain.Transaction
		want bool
	}{
		{name: "missing transaction", tx: nil},
		{name: "no link yet", tx: &domain.Transaction{Status: domain.TransactionStatusPending}},
		{name: "blank link", tx: &domain.Transaction{Status: domain.TransactionStatusPending, CheckoutSessionURL: strPtr("")}},
		{name: "expired status", tx: &domain.Transaction{Status: domain.TransactionStatusExpired, CheckoutSessionURL: &link}},
		{name: "paid status", tx: &domain.Transaction{Status: domain.TransactionStatusPaid, CheckoutSessionURL: &link}},
		{name: "expired deadline", tx: &domain.Transaction{Status: domain.TransactionStatusPending, CheckoutSessionURL: &link, InvoiceExpiresAt: &past}},
		{name: "valid deadline", tx: &domain.Transaction{Status: domain.TransactionStatusPending, CheckoutSessionURL: &link, InvoiceExpiresAt: &future}, want: true},
		{name: "unknown deadline", tx: &domain.Transaction{Status: domain.TransactionStatusPending, CheckoutSessionURL: &link}, want: true},
		{name: "pending retry state", tx: &domain.Transaction{Status: "creating", CheckoutSessionURL: &link}, want: true},
		{name: "failed status", tx: &domain.Transaction{Status: domain.TransactionStatusFailed, CheckoutSessionURL: &link}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, ok := reusableInvoiceResponse(test.tx, time.Now())
			if ok != test.want {
				t.Fatalf("reusableInvoiceResponse() = %v, want %v", ok, test.want)
			}
			if ok && response.CheckoutSessionURL != link {
				t.Fatalf("checkout session URL = %q, want %q", response.CheckoutSessionURL, link)
			}
		})
	}
}

func TestInvoiceValidityMinutesFallsBackToDefault(t *testing.T) {
	usecase := newTransactionUsecaseForTest(&transactionRepoStub{missing: true}, &reconciliationRepoStub{}, &invoiceGatewayStub{}, &academicActivationStub{}, expiryTestConfig())
	concrete, ok := usecase.(*transactionUsecase)
	if !ok {
		t.Fatalf("usecase type = %T, want *transactionUsecase", usecase)
	}
	if got := concrete.invoiceValidityMinutes(); got != 7*24*60 {
		t.Fatalf("invoiceValidityMinutes() = %d, want %d", got, 7*24*60)
	}

	fallback := NewTransactionUsecaseWithReconciliation(
		&transactionRepoStub{missing: true}, &walletRepoStub{}, &ledgerRepoStub{}, &subscriptionRepoStub{},
		&invoiceGatewayStub{}, &academicActivationStub{}, config.Config{}, nil, &reconciliationRepoStub{},
	).(*transactionUsecase)
	if got := fallback.invoiceValidityMinutes(); got != 14*24*60 {
		t.Fatalf("invoiceValidityMinutes() without configuration = %d, want %d", got, 14*24*60)
	}
}

func TestIsKnownResultCode(t *testing.T) {
	tests := map[string]bool{"00": true, "01": true, "02": true, "": false, "77": false, "0": false}
	for code, want := range tests {
		if got := domain.IsKnownResultCode(code); got != want {
			t.Fatalf("IsKnownResultCode(%q) = %v, want %v", code, got, want)
		}
	}
}

func TestInvoiceExpiresAtUsesRequestedValidity(t *testing.T) {
	now := time.Date(2026, time.September, 20, 10, 0, 0, 0, time.UTC)
	if got := domain.InvoiceExpiresAt(now, 60); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("InvoiceExpiresAt(now, 60) = %v, want %v", got, now.Add(time.Hour))
	}
	if got := domain.InvoiceExpiresAt(now, 0); !got.Equal(now.Add(domain.DefaultInvoiceValidityMinutes * time.Minute)) {
		t.Fatalf("InvoiceExpiresAt(now, 0) = %v, want the default validity", got)
	}
	if got := domain.InvoiceExpiresAt(now, -5); !got.Equal(now.Add(domain.DefaultInvoiceValidityMinutes * time.Minute)) {
		t.Fatalf("InvoiceExpiresAt(now, -5) = %v, want the default validity", got)
	}
}

func strPtr(value string) *string        { return &value }
func timePtr(value time.Time) *time.Time { return &value }
