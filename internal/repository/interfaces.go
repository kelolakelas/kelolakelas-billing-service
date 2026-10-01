package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type WalletRepository interface {
	GetByTenantID(ctx context.Context, tenantID uuid.UUID) (*domain.Wallet, error)
	Create(ctx context.Context, wallet *domain.Wallet) error
	Update(ctx context.Context, wallet *domain.Wallet) error
}

type WalletLockingRepository interface {
	GetByTenantIDForUpdate(ctx context.Context, tenantID uuid.UUID) (*domain.Wallet, error)
}

type LedgerEntryRepository interface {
	Create(ctx context.Context, entry *domain.LedgerEntry) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.LedgerEntry, error)
	// ListByWallet returns one wallet's entries newest first; page starts at 1.
	ListByWallet(ctx context.Context, walletID uuid.UUID, page, pageSize int) ([]domain.LedgerEntry, int64, error)
	// SumByWallet totals one wallet's entries; it is the ledger side of the
	// balance invariant the paid callback maintains.
	SumByWallet(ctx context.Context, walletID uuid.UUID) (int64, error)
}

type BankAccountRepository interface {
	Create(ctx context.Context, account *domain.BankAccount) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.BankAccount, error)
	// GetByIDScoped reads one account inside its tenant; anything else is
	// gorm.ErrRecordNotFound so callers cannot distinguish missing, deleted,
	// or foreign rows.
	GetByIDScoped(ctx context.Context, tenantID, id uuid.UUID) (*domain.BankAccount, error)
	// GetByIDScopedForUpdate is the locked variant for write transactions.
	GetByIDScopedForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.BankAccount, error)
	// ListByTenant returns the tenant's live accounts, primary first.
	ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.BankAccount, error)
	// HasPrimary reports whether the tenant already has a live primary account.
	HasPrimary(ctx context.Context, tenantID uuid.UUID) (bool, error)
	// ClearPrimary demotes every live primary of the tenant. It is one
	// statement, so it never leaves a half-moved slot behind.
	ClearPrimary(ctx context.Context, tenantID uuid.UUID) error
	// SetPrimary makes id the tenant's only primary: it clears the slot first
	// and then promotes the target, so a crash between the two cannot strand
	// two primaries. Run it inside a transaction after locking the target.
	SetPrimary(ctx context.Context, tenantID, id uuid.UUID) error
	Update(ctx context.Context, account *domain.BankAccount) error
	// SoftDelete marks the row deleted; history rows are never removed.
	SoftDelete(ctx context.Context, tenantID, id uuid.UUID) (bool, error)
}

type WithdrawalRepository interface {
	Create(ctx context.Context, withdrawal *domain.Withdrawal) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Withdrawal, error)
	// GetByIDScoped reads one withdrawal inside its tenant; anything else is
	// gorm.ErrRecordNotFound so callers cannot distinguish missing or foreign
	// rows.
	GetByIDScoped(ctx context.Context, tenantID, id uuid.UUID) (*domain.Withdrawal, error)
	Update(ctx context.Context, withdrawal *domain.Withdrawal) error
	// CountActiveByBankAccount counts withdrawals in the given statuses that
	// still reference the account; a non-zero count blocks deleting it.
	CountActiveByBankAccount(ctx context.Context, accountID uuid.UUID, statuses []string) (int64, error)
	// CountOpenByTenant counts the tenant's withdrawals that still hold
	// balance; it runs under the caller's wallet row lock so concurrent
	// requests serialize on the lock for the single-open guard.
	CountOpenByTenant(ctx context.Context, tenantID uuid.UUID) (int64, error)
	// GetByTenantAndKey returns the tenant's request stored under the
	// idempotency key, or gorm.ErrRecordNotFound when never used.
	GetByTenantAndKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Withdrawal, error)
	// ListByTenant returns one tenant's withdrawals newest first; page starts
	// at 1.
	ListByTenant(ctx context.Context, tenantID uuid.UUID, page, pageSize int) ([]domain.Withdrawal, int64, error)
	// ClaimCancelled moves a `requested` withdrawal of the tenant to
	// `cancelled` in one conditional statement and reports whether a row was
	// actually moved, so concurrent cancellers (or a cancel racing a future
	// admin processing step) have exactly one winner.
	ClaimCancelled(ctx context.Context, tenantID, id uuid.UUID, now time.Time) (bool, error)
}

// PlatformWithdrawalRepository is separate to keep existing tenant test doubles
// and tenant-scoped repository contracts unchanged.
type PlatformWithdrawalRepository interface {
	ListRequested(ctx context.Context, page, pageSize int) ([]domain.Withdrawal, int64, error)
	ClaimDecision(ctx context.Context, id, adminID uuid.UUID, status, detail string, now time.Time) (bool, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Withdrawal, error)
}

type VoucherRepository interface {
	// LockTenantCodes serializes code writes for this tenant within the caller's transaction.
	LockTenantCodes(ctx context.Context, tenantID uuid.UUID) error
	Create(ctx context.Context, voucher *domain.Voucher) error
	// GetByIDScoped reads one voucher inside its tenant; a missing row, a
	// soft-deleted row, or a row of another tenant is
	// gorm.ErrRecordNotFound so callers cannot distinguish them.
	GetByIDScoped(ctx context.Context, tenantID, id uuid.UUID) (*domain.Voucher, error)
	// GetByCode matches the code case-insensitively inside the tenant, or
	// gorm.ErrRecordNotFound when the tenant has no live voucher under it.
	GetByCode(ctx context.Context, tenantID uuid.UUID, code string) (*domain.Voucher, error)
	// HasCodeOtherThan checks all matching live rows, excluding the row being renamed.
	HasCodeOtherThan(ctx context.Context, tenantID, excludeID uuid.UUID, code string) (bool, error)
	// GetByIDScopedForUpdate is the locked variant for write transactions.
	GetByIDScopedForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Voucher, error)
	// ListByTenant returns the tenant's live vouchers newest first; page
	// starts at 1.
	ListByTenant(ctx context.Context, tenantID uuid.UUID, page, pageSize int) ([]domain.Voucher, int64, error)
	Update(ctx context.Context, voucher *domain.Voucher) error
	// SoftDelete marks the row deleted; history rows are never removed. Only
	// vouchers that never discounted a transaction reach here.
	SoftDelete(ctx context.Context, tenantID, id uuid.UUID) (bool, error)
}

type TransactionRepository interface {
	Create(ctx context.Context, transaction *domain.Transaction) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Transaction, error)
	GetByMerchantOrderID(ctx context.Context, merchantOrderID string) (*domain.Transaction, error)
	GetByEnrollmentID(ctx context.Context, enrollmentID uuid.UUID) (*domain.Transaction, error)
	GetBySubscriptionPeriod(ctx context.Context, subscriptionID uuid.UUID, period time.Time) (*domain.Transaction, error)
	GetByPaymentIntentID(ctx context.Context, paymentIntentID string) (*domain.Transaction, error)
	List(ctx context.Context, tenantID *uuid.UUID, parentID *uuid.UUID, query domain.TransactionQuery) ([]domain.Transaction, int64, error)
	Update(ctx context.Context, transaction *domain.Transaction) error
}

// SalesSummaryRepository is separate so legacy transaction test doubles need not implement it.
type SalesSummaryRepository interface {
	SummarizePaid(ctx context.Context, tenantID uuid.UUID, from, until time.Time) ([]domain.SalesSummary, error)
}

// TransactionExportRepository streams one tenant's filtered transactions for
// the CSV export. It is separate so legacy transaction test doubles need not
// implement it, mirroring SalesSummaryRepository.
type TransactionExportRepository interface {
	// IterateExport calls fn with successive batches of at most batchSize
	// rows, oldest first, and stops when the range is drained. The tenant
	// scope is always applied; query carries the status, student, enrollment,
	// search, and date filters shared with the list endpoint.
	IterateExport(ctx context.Context, tenantID uuid.UUID, query domain.TransactionQuery, batchSize int, fn func([]domain.Transaction) error) error
}

type TransactionLockingRepository interface {
	GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Transaction, error)
	// ClaimInvoice and ClaimReinvoice take exclusive ownership of creating an invoice
	// by moving the row to `creating`. Both are conditional updates, so of any number
	// of concurrent requests exactly one reports true and the others read the state
	// the winner is producing instead of calling the provider again.
	//
	// Ownership is never permanent: a claim is released by RestoreFailedInvoiceClaim
	// when the creating request reports a provider failure, and becomes reclaimable
	// once claimTimeoutMinutes have passed since invoice_claimed_at when the claiming
	// process died without reporting back. Passing a non-positive timeout applies
	// domain.DefaultTransactionClaimTimeoutMinutes.
	ClaimInvoice(ctx context.Context, id uuid.UUID, now time.Time, claimTimeoutMinutes int) (bool, error)
	ClaimReinvoice(ctx context.Context, id uuid.UUID, now time.Time, claimTimeoutMinutes int) (bool, error)
	// RestoreFailedInvoiceClaim returns a `creating` row to `failed` and records why
	// the invoice creation failed. It matches nothing when the caller no longer owns
	// the row, which is what keeps a late success or a concurrent cancellation safe.
	RestoreFailedInvoiceClaim(ctx context.Context, id uuid.UUID, reason string, now time.Time) (bool, error)
	ClaimPaymentLinkEmail(ctx context.Context, id uuid.UUID, sentAt time.Time) (bool, error)
	// ReleasePaymentLinkEmailClaim only clears this worker's stamp on a still-pending row.
	// It must not rewrite settlement state or release another worker's newer claim.
	ReleasePaymentLinkEmailClaim(ctx context.Context, id uuid.UUID, sentAt time.Time) (bool, error)
	ClaimReminderEmail(ctx context.Context, id uuid.UUID, sentAt time.Time, intervalDays int) (bool, error)
	ClaimOutcomeEmail(ctx context.Context, id uuid.UUID, status string, sentAt time.Time) (bool, error)
	ReleaseOutcomeEmailClaim(ctx context.Context, id uuid.UUID, status string, sentAt time.Time) (bool, error)
	// CancelUnpaid marks the unpaid transactions of a withdrawn enrollment as
	// cancelled, and MarkInvoiceIssued stores a freshly created payment link only
	// while the transaction is still awaiting one. Both are conditional updates so
	// a cancellation and an in-flight invoice creation can never resurrect each
	// other: whichever statement runs second matches no row and reports false.
	CancelUnpaid(ctx context.Context, id uuid.UUID) (bool, error)
	MarkInvoiceIssued(ctx context.Context, id uuid.UUID, invoice *domain.CreateInvoiceResponse, expiresAt time.Time) (bool, error)
}

// PrivatePaymentEmailRepository is separate from renewal claims. The claim and
// completion updates are conditional, so concurrent dispatchers cannot overwrite
// a settled payment or another worker's claim.
type PrivatePaymentEmailRepository interface {
	ListPrivatePaymentEmails(ctx context.Context, now time.Time, limit int) ([]domain.Transaction, error)
	ClaimPrivatePaymentEmail(ctx context.Context, id uuid.UUID, now time.Time, timeout time.Duration) (bool, error)
	FinishPrivatePaymentEmail(ctx context.Context, id uuid.UUID, claimedAt time.Time, failure string) (bool, error)
}

// TransactionExpiryRepository expires unpaid transactions whose invoice validity
// window has passed. ExpireDue must be safe to run concurrently from several
// replicas: rows are claimed with a single conditional update so a transaction
// moves to `expired` at most once. Expiring also enqueues the durable Academic
// seat release in the same statement, so a seat is never left locked without a
// retry job recording why.
type TransactionExpiryRepository interface {
	ExpireDue(ctx context.Context, now time.Time, limit int) (int64, error)
}

type PaymentReconciliationRepository interface {
	EnsureActivation(ctx context.Context, reconciliation *domain.PaymentReconciliation) error
	EnqueueRelease(ctx context.Context, reconciliation *domain.PaymentReconciliation) error
	CancelPendingRelease(ctx context.Context, transactionID uuid.UUID) error
	GetByTransactionID(ctx context.Context, transactionID uuid.UUID) (*domain.PaymentReconciliation, error)
	ClaimDue(ctx context.Context, transactionID uuid.UUID, now time.Time, lease time.Duration) (*domain.PaymentReconciliation, error)
	MarkActive(ctx context.Context, id uuid.UUID, completedAt time.Time) error
	MarkRetry(ctx context.Context, id uuid.UUID, nextAttemptAt time.Time, lastError string, maxAttempts int) error
	ListByStatus(ctx context.Context, status string, limit int) ([]domain.PaymentReconciliation, error)
	RequeueTerminalFailed(ctx context.Context, now time.Time, limit int) (int64, error)
}

type BillingTransactionManager interface {
	WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

type SubscriptionRepository interface {
	Create(ctx context.Context, subscription *domain.Subscription) error
	GetByEnrollmentID(ctx context.Context, enrollmentID uuid.UUID) (*domain.Subscription, error)
	Update(ctx context.Context, subscription *domain.Subscription) error
	ListDueForRenewal(ctx context.Context, before time.Time) ([]domain.Subscription, error)
}
