package repository

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type transactionRepository struct {
	db *gorm.DB
}

// expiredTransactionRelease is one row of the ExpireDue result: the expired transaction,
// its enrollment, and whether this statement is the one that inserted its release job.
// The flag is what keeps the transition log honest when ON CONFLICT skips the insert.
type expiredTransactionRelease struct {
	ID           uuid.UUID `gorm:"column:id"`
	EnrollmentID uuid.UUID `gorm:"column:enrollment_id"`
	Released     bool      `gorm:"column:released"`
}

func NewTransactionRepository(db *gorm.DB) TransactionRepository {
	return &transactionRepository{db: db}
}

func (r *transactionRepository) Create(ctx context.Context, transaction *domain.Transaction) error {
	return r.getDB(ctx).Create(transaction).Error
}

func (r *transactionRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := r.getDB(ctx).Preload("Reconciliation").First(&tx, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &tx, nil
}

func (r *transactionRepository) GetByMerchantOrderID(ctx context.Context, merchantOrderID string) (*domain.Transaction, error) {
	var tx domain.Transaction
	err := r.getDB(ctx).Preload("Reconciliation").First(&tx, "merchant_order_id = ?", merchantOrderID).Error
	return &tx, err
}

func (r *transactionRepository) GetByEnrollmentID(ctx context.Context, enrollmentID uuid.UUID) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := r.getDB(ctx).Where("enrollment_id = ?", enrollmentID).Order("created_at DESC").First(&tx).Error; err != nil {
		return nil, err
	}
	return &tx, nil
}

func (r *transactionRepository) GetBySubscriptionPeriod(ctx context.Context, subscriptionID uuid.UUID, period time.Time) (*domain.Transaction, error) {
	var tx domain.Transaction
	err := r.getDB(ctx).Where("subscription_id = ? AND billing_period_start = ?", subscriptionID, period).First(&tx).Error
	return &tx, err
}

func (r *transactionRepository) GetByPaymentIntentID(ctx context.Context, paymentIntentID string) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := r.getDB(ctx).First(&tx, "payment_intent_id = ?", paymentIntentID).Error; err != nil {
		return nil, err
	}
	return &tx, nil
}

func (r *transactionRepository) List(ctx context.Context, tenantID, parentID *uuid.UUID, query domain.TransactionQuery) ([]domain.Transaction, int64, error) {
	db := r.db.WithContext(ctx).Model(&domain.Transaction{}).Preload("Reconciliation")
	if tenantID != nil {
		db = db.Where("tenant_id = ?", *tenantID)
	}
	if parentID != nil {
		db = db.Where("parent_id = ?", *parentID)
	}
	if query.Status != "" {
		db = db.Where("status = ?", query.Status)
	}
	if query.StudentID != nil {
		db = db.Where("student_id = ?", *query.StudentID)
	}
	if query.EnrollmentID != nil {
		db = db.Where("enrollment_id = ?", *query.EnrollmentID)
	}
	if query.Search != "" {
		db = db.Where("merchant_order_id ILIKE ? OR payment_intent_id ILIKE ?", "%"+query.Search+"%", "%"+query.Search+"%")
	}
	if query.DateFrom != nil {
		db = db.Where("created_at >= ?", *query.DateFrom)
	}
	if query.DateTo != nil {
		db = db.Where("created_at <= ?", *query.DateTo)
	}
	var total int64
	if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []domain.Transaction
	err := db.Order("created_at DESC").Limit(query.PageSize).Offset((query.Page - 1) * query.PageSize).Find(&items).Error
	return items, total, err
}

func (r *transactionRepository) Update(ctx context.Context, transaction *domain.Transaction) error {
	return r.getDB(ctx).Save(transaction).Error
}

func (r *transactionRepository) getDB(ctx context.Context) *gorm.DB { return GetDB(ctx, r.db) }

func (r *transactionRepository) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := r.getDB(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&tx, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &tx, nil
}

// ClaimInvoice takes exclusive ownership of creating an invoice for a transaction.
// Exactly one caller can win: the statement only matches a row that is still waiting
// for an invoice — `pending`/`failed` with no payment link — or a `creating` row whose
// claim is older than the timeout, which is how a claim left behind by a process that
// died mid-call becomes recoverable instead of blocking the enrollment forever.
//
// A `creating` row that also holds a checkout URL is never matched. That combination
// means the provider did answer and the link was stored; reclaiming it would create a
// second invoice for the same merchant order ID.
//
// The claim timestamp is refreshed on every acquisition so the age always measures the
// current claim, and the failure reason of the previous attempt is cleared because the
// caller is about to make a fresh one.
func (r *transactionRepository) ClaimInvoice(ctx context.Context, id uuid.UUID, now time.Time, claimTimeoutMinutes int) (bool, error) {
	if claimTimeoutMinutes <= 0 {
		claimTimeoutMinutes = domain.DefaultTransactionClaimTimeoutMinutes
	}
	staleBefore := now.Add(-time.Duration(claimTimeoutMinutes) * time.Minute)
	result := r.getDB(ctx).Model(&domain.Transaction{}).
		Where(`id = ? AND checkout_session_url IS NULL AND (
			status IN ? OR (
				status = ? AND (invoice_claimed_at IS NULL OR invoice_claimed_at <= ?)
			)
		)`, id, []string{domain.TransactionStatusPending, domain.TransactionStatusFailed}, domain.TransactionStatusCreating, staleBefore).
		Updates(map[string]interface{}{
			"status":                 domain.TransactionStatusCreating,
			"invoice_claimed_at":     now,
			"invoice_failure_reason": nil,
			"updated_at":             now,
		})
	return result.RowsAffected == 1, result.Error
}

// RestoreFailedInvoiceClaim returns a transaction to a claimable status after the
// provider call failed, recording why it failed. The restore is deliberately narrower
// than the claim: it only matches the `creating` row the caller still owns, so an
// invoice that was issued in the meantime is never pulled back to `failed` and a
// cancellation that won the race is never overwritten. The payment link is checked as
// well, because a response that arrived late means the invoice really does exist.
//
// The row keeps its payment link (none can be present, by the predicate above) and is
// left with `invoice_failure_reason` set, which is the retry history an operator reads
// while the next attempt clears it.
func (r *transactionRepository) RestoreFailedInvoiceClaim(ctx context.Context, id uuid.UUID, reason string, now time.Time) (bool, error) {
	result := r.getDB(ctx).Model(&domain.Transaction{}).
		Where("id = ? AND status = ? AND checkout_session_url IS NULL", id, domain.TransactionStatusCreating).
		Updates(map[string]interface{}{
			"status":                 domain.TransactionStatusFailed,
			"invoice_failure_reason": reason,
			"updated_at":             now,
		})
	return result.RowsAffected == 1, result.Error
}

// ClaimReinvoice takes ownership of issuing a replacement invoice for a transaction
// whose invoice can no longer be paid. Only `expired` transactions, unpaid
// transactions that never received a payment link, and unpaid transactions whose
// stored expiry has already passed can be claimed; a transaction that has been
// paid, or whose payment link is still valid, never matches. The previous payment
// link and payment intent are cleared so the stale invoice is never served again,
// and the stored expiry stays in place until the replacement invoice is created.
//
// A row stranded in `creating` is handled here too: an abandoned claim with no payment
// link has nothing left to pay, so it is released to `failed` and claimed in the same
// statement. Without that arm the enrollment would be stuck in `creating` forever
// whenever the replacement invoice never completed.
func (r *transactionRepository) ClaimReinvoice(ctx context.Context, id uuid.UUID, now time.Time, claimTimeoutMinutes int) (bool, error) {
	if claimTimeoutMinutes <= 0 {
		claimTimeoutMinutes = domain.DefaultTransactionClaimTimeoutMinutes
	}
	staleBefore := now.Add(-time.Duration(claimTimeoutMinutes) * time.Minute)
	result := r.getDB(ctx).Model(&domain.Transaction{}).
		Where(`id = ? AND (
			status = ? OR (
				status IN ? AND (
					checkout_session_url IS NULL OR
					(invoice_expires_at IS NOT NULL AND invoice_expires_at <= ?)
				)
			) OR (
				status = ? AND checkout_session_url IS NULL AND
				(invoice_claimed_at IS NULL OR invoice_claimed_at <= ?)
			)
		)`, id, domain.TransactionStatusExpired,
			[]string{domain.TransactionStatusPending, domain.TransactionStatusFailed}, now,
			domain.TransactionStatusCreating, staleBefore).
		Updates(map[string]interface{}{
			"status":                 domain.TransactionStatusCreating,
			"checkout_session_url":   nil,
			"payment_intent_id":      nil,
			"invoice_claimed_at":     now,
			"invoice_failure_reason": nil,
			"updated_at":             now,
		})
	return result.RowsAffected == 1, result.Error
}

// ExpireDue moves due unpaid transactions to the terminal `expired` status in a
// single conditional update. The inner SELECT uses FOR UPDATE SKIP LOCKED so
// concurrent replicas claim disjoint rows, and the outer WHERE re-checks the
// status so an invoice paid in the meantime is never expired.
// ExpireDue marks due unpaid transactions as expired and enqueues their durable
// Academic seat release in one statement, so there is no window in which a seat is
// locked without a retry job recording why. The release job is inserted with
// ON CONFLICT DO NOTHING against the unique transaction_id: a transaction whose
// activation is still owed keeps that job, and a transaction that already activated
// keeps its accepted activation. The statement returns one row per expired
// transaction — not per inserted job — so callers can drain a backlog in batches.
//
// The `released` CTE is joined to the returned rows so each inserted release job can be
// logged with its transaction and enrollment. The join is a left join on purpose: a
// transaction whose activation already owns the unique transaction_id inserted nothing,
// and reporting that skip as a transition would announce a seat release that never
// happened.
func (r *transactionRepository) ExpireDue(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	var expired []expiredTransactionRelease
	err := r.getDB(ctx).Raw(`
		WITH due AS (
			SELECT id FROM transactions
			WHERE status = ? AND deleted_at IS NULL AND invoice_expires_at IS NOT NULL AND invoice_expires_at <= ?
			ORDER BY invoice_expires_at
			LIMIT ?
			FOR UPDATE SKIP LOCKED
		), expired AS (
			UPDATE transactions t
			SET status = ?, expired_at = ?, updated_at = ?
			FROM due
			WHERE t.id = due.id AND t.status = ?
			RETURNING t.id, t.enrollment_id
		), released AS (
			INSERT INTO payment_reconciliations (id, transaction_id, enrollment_id, kind, status, attempt_count, next_attempt_at, created_at, updated_at)
			SELECT gen_random_uuid(), id, enrollment_id, ?, ?, 0, ?, ?, ?
			FROM expired
			WHERE enrollment_id <> ?
			ON CONFLICT (transaction_id) DO NOTHING
			RETURNING transaction_id
		)
		SELECT expired.id, expired.enrollment_id, released.transaction_id IS NOT NULL AS released
		FROM expired
		LEFT JOIN released ON released.transaction_id = expired.id`,
		domain.TransactionStatusPending, now, limit,
		domain.TransactionStatusExpired, now, now, domain.TransactionStatusPending,
		domain.ReconciliationKindRelease, domain.ReconciliationStatusPending, now, now, now,
		uuid.Nil).Scan(&expired).Error
	if err != nil {
		return 0, err
	}
	for i := range expired {
		if !expired[i].Released {
			continue
		}
		logReconciliationTransition(ctx, slog.LevelInfo, "enqueued enrollment seat release", &domain.PaymentReconciliation{
			TransactionID: expired[i].ID,
			EnrollmentID:  expired[i].EnrollmentID,
			Kind:          domain.ReconciliationKindRelease,
			Status:        domain.ReconciliationStatusPending,
		}, domain.ReconciliationStatusPending)
	}
	return int64(len(expired)), nil
}

// CancelUnpaid marks the unpaid transactions of a withdrawn enrollment as
// cancelled and withdraws any seat release that has not been claimed yet, in one
// statement. A transaction that already settled is never rewritten: `paid` and
// `refunded` are excluded, so money that arrived is never hidden behind a
// cancellation. `creating` is cancellable on purpose — a transaction stuck there is
// the record of an invoice generation that never completed, and without this the
// enrollment could never be cancelled — while MarkInvoiceIssued refuses to write a
// payment link onto the row this statement just cancelled. The seat release job is
// inserted with ON CONFLICT DO NOTHING, so a release already accepted or already
// claimed is left untouched: the seat really is free in that case, and the Academic
// cancellation is what makes the state consistent, not a second job.
//
// Like ExpireDue, the statement returns the inserted release job alongside the cancelled
// transaction so the enqueue is logged with both ids, and a release job that lost the
// unique-index race is reported as skipped rather than as a new transition.
func (r *transactionRepository) CancelUnpaid(ctx context.Context, id uuid.UUID) (bool, error) {
	now := time.Now()
	var cancelled []expiredTransactionRelease
	err := r.getDB(ctx).Raw(`
		WITH target AS (
			UPDATE transactions
			SET status = ?, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL
				AND (status = ? OR status = ? OR status = ? OR status = ?)
			RETURNING id, enrollment_id
		), released AS (
			INSERT INTO payment_reconciliations (id, transaction_id, enrollment_id, kind, status, attempt_count, next_attempt_at, created_at, updated_at)
			SELECT gen_random_uuid(), id, enrollment_id, ?, ?, 0, ?, ?, ?
			FROM target
			WHERE enrollment_id <> ?
			ON CONFLICT (transaction_id) DO NOTHING
			RETURNING transaction_id
		)
		SELECT target.id, target.enrollment_id, released.transaction_id IS NOT NULL AS released
		FROM target
		LEFT JOIN released ON released.transaction_id = target.id`,
		domain.TransactionStatusCancelled, now,
		id,
		domain.TransactionStatusPending, domain.TransactionStatusCreating, domain.TransactionStatusFailed, domain.TransactionStatusExpired,
		domain.ReconciliationKindRelease, domain.ReconciliationStatusPending, now, now, now,
		uuid.Nil).Scan(&cancelled).Error
	if err != nil {
		return false, err
	}
	for i := range cancelled {
		if !cancelled[i].Released {
			continue
		}
		logReconciliationTransition(ctx, slog.LevelInfo, "enqueued enrollment seat release", &domain.PaymentReconciliation{
			TransactionID: cancelled[i].ID,
			EnrollmentID:  cancelled[i].EnrollmentID,
			Kind:          domain.ReconciliationKindRelease,
			Status:        domain.ReconciliationStatusPending,
		}, domain.ReconciliationStatusPending)
	}
	return len(cancelled) > 0, nil
}

// MarkInvoiceIssued stores a payment link on a transaction that is still waiting for
// one. The status guard re-checks the row at write time, so a cancellation that won
// the race is never overwritten by the invoice creation it was racing: the caller
// learns it no longer owns the row and the withdrawal wins, which is the safe
// outcome because the parent asked to stop paying. Only `creating` and `pending`
// rows match, so a paid transaction is never moved back to awaiting payment.
//
// The claim bookkeeping is cleared in the same write: the row is no longer being
// created, so it holds neither an outstanding claim nor a failure reason.
func (r *transactionRepository) MarkInvoiceIssued(ctx context.Context, id uuid.UUID, checkoutSessionURL, paymentIntentID string, expiresAt time.Time) (bool, error) {
	result := r.getDB(ctx).Model(&domain.Transaction{}).
		Where("id = ? AND status IN ?", id, []string{domain.TransactionStatusCreating, domain.TransactionStatusPending}).
		Updates(map[string]interface{}{
			"status":                 domain.TransactionStatusPending,
			"expired_at":             nil,
			"invoice_expires_at":     expiresAt,
			"checkout_session_url":   checkoutSessionURL,
			"payment_intent_id":      paymentIntentID,
			"invoice_claimed_at":     nil,
			"invoice_failure_reason": nil,
			"updated_at":             time.Now(),
		})
	return result.RowsAffected == 1, result.Error
}

func (r *transactionRepository) ClaimPaymentLinkEmail(ctx context.Context, id uuid.UUID, sentAt time.Time) (bool, error) {
	result := r.getDB(ctx).Model(&domain.Transaction{}).Where("id = ? AND payment_link_sent_at IS NULL AND status = ?", id, "pending").Update("payment_link_sent_at", sentAt)
	return result.RowsAffected == 1, result.Error
}

func (r *transactionRepository) ClaimReminderEmail(ctx context.Context, id uuid.UUID, sentAt time.Time, intervalDays int) (bool, error) {
	cutoff := sentAt.AddDate(0, 0, -intervalDays)
	result := r.getDB(ctx).Model(&domain.Transaction{}).Where("id = ? AND status = 'pending' AND (last_reminder_sent_at IS NULL OR last_reminder_sent_at <= ?)", id, cutoff).
		Updates(map[string]interface{}{"last_reminder_sent_at": sentAt, "reminder_count": gorm.Expr("reminder_count + 1")})
	return result.RowsAffected == 1, result.Error
}
