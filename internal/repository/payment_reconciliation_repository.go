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

type paymentReconciliationRepository struct{ db *gorm.DB }

func NewPaymentReconciliationRepository(db *gorm.DB) *paymentReconciliationRepository {
	return &paymentReconciliationRepository{db: db}
}

// logReconciliationTransition records a persisted state change on a reconciliation
// row. Every transition is written here, next to the statement that actually changed
// the row, because this is the only layer that knows the real stored outcome: a
// conditional update that matched no row or an insert skipped by ON CONFLICT is not a
// transition and must not be logged as one. The transaction and enrollment ids are the
// correlation keys an operator has from the parent-visible response, and the failure
// detail is the same redacted message the API already returns — no credential or
// provider payload is added.
func logReconciliationTransition(ctx context.Context, level slog.Level, message string, reconciliation *domain.PaymentReconciliation, status string, attrs ...any) {
	if reconciliation == nil {
		return
	}
	args := []any{
		"transaction_id", reconciliation.TransactionID,
		"enrollment_id", reconciliation.EnrollmentID,
		"kind", reconciliation.Kind,
		"status", status,
		"attempt_count", reconciliation.AttemptCount,
	}
	args = append(args, attrs...)
	slog.Log(ctx, level, message, args...)
}

// EnsureActivation records that a paid transaction still owes Academic an
// activation. A row that already completed an activation is left untouched, while
// a row that was turned into a release job is converted back: the seat may already
// be gone, so the attempt must stay visible instead of being swallowed.
//
// Only a row this statement actually inserted or converted is logged: the WHERE on
// the conflict update deliberately skips a row whose activation already succeeded, and
// that skip is not a transition. A replay of the paid callback therefore does not
// produce a second "pending" line for a job that is already done.
func (r *paymentReconciliationRepository) EnsureActivation(ctx context.Context, reconciliation *domain.PaymentReconciliation) error {
	db := GetDB(ctx, r.db)
	result := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "transaction_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"kind":            reconciliation.Kind,
			"status":          reconciliation.Status,
			"next_attempt_at": reconciliation.NextAttemptAt,
			"last_error":      nil,
		}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Or(
				clause.Neq{Column: clause.Column{Table: "payment_reconciliations", Name: "kind"}, Value: domain.ReconciliationKindActivation},
				clause.Neq{Column: clause.Column{Table: "payment_reconciliations", Name: "status"}, Value: domain.ReconciliationStatusActive},
			),
		}},
	}).Create(reconciliation)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		logReconciliationTransition(ctx, slog.LevelInfo, "enqueued enrollment reconciliation", reconciliation, domain.ReconciliationStatusPending)
	}
	return nil
}

// EnqueueRelease records that a failed or expired transaction must release its
// Academic seat. Rows that already owe Academic something — including a completed
// activation — are never overwritten here, because a failure notification must not
// revoke a seat the activation just confirmed. Expiry calls this only after the row
// is already `expired`, which is precisely the state that owes a release.
//
// A conflict means the transaction already owes Academic something else; nothing was
// written, so nothing is logged as a new transition.
func (r *paymentReconciliationRepository) EnqueueRelease(ctx context.Context, reconciliation *domain.PaymentReconciliation) error {
	db := GetDB(ctx, r.db)
	result := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "transaction_id"}}, DoNothing: true}).Create(reconciliation)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		logReconciliationTransition(ctx, slog.LevelInfo, "enqueued enrollment seat release", reconciliation, domain.ReconciliationStatusPending)
	}
	return nil
}

// CancelPendingRelease withdraws a release job that has not been claimed yet, which
// the re-invoice path uses so a replacement invoice cannot race the worker into
// dropping a seat the parent is about to pay for. The row is deleted rather than
// re-kinded: a withdrawn job owes Academic nothing, and re-using the row as an
// activation would let the worker confirm a seat for an invoice that is still
// unpaid. Only `pending` rows are affected — a job already in flight or already
// accepted describes a seat that is genuinely released, and that evidence is kept so
// a later paid callback surfaces the rejection instead of hiding it.
func (r *paymentReconciliationRepository) CancelPendingRelease(ctx context.Context, transactionID uuid.UUID) error {
	return GetDB(ctx, r.db).
		Where("transaction_id = ? AND kind = ? AND status = ?", transactionID, domain.ReconciliationKindRelease, domain.ReconciliationStatusPending).
		Delete(&domain.PaymentReconciliation{}).Error
}

func (r *paymentReconciliationRepository) GetByTransactionID(ctx context.Context, transactionID uuid.UUID) (*domain.PaymentReconciliation, error) {
	var reconciliation domain.PaymentReconciliation
	err := GetDB(ctx, r.db).Where("transaction_id = ?", transactionID).First(&reconciliation).Error
	if err != nil {
		return nil, err
	}
	return &reconciliation, nil
}

func (r *paymentReconciliationRepository) ClaimDue(ctx context.Context, transactionID uuid.UUID, now time.Time, lease time.Duration) (*domain.PaymentReconciliation, error) {
	var reconciliation domain.PaymentReconciliation
	leaseCutoff := now.Add(-lease)
	err := GetDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		query := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		if transactionID != uuid.Nil {
			query = query.Where("transaction_id = ?", transactionID)
		}
		// The paid path claims by transaction id and must only ever dispatch the
		// activation it is paying for; release jobs are drained by the worker's
		// kind-agnostic sweep.
		if transactionID != uuid.Nil {
			query = query.Where("kind = ?", domain.ReconciliationKindActivation)
		}
		err := query.
			Where("(status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)) OR (status = ? AND last_attempt_at < ?)",
				domain.ReconciliationStatusPending, now, domain.ReconciliationStatusProcessing, leaseCutoff).
			Order("created_at ASC").First(&reconciliation).Error
		if err != nil {
			return err
		}
		return tx.Model(&domain.PaymentReconciliation{}).Where("id = ?", reconciliation.ID).Updates(map[string]interface{}{
			"status":          domain.ReconciliationStatusProcessing,
			"attempt_count":   gorm.Expr("attempt_count + 1"),
			"last_attempt_at": now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	reconciliation.Status = domain.ReconciliationStatusProcessing
	reconciliation.AttemptCount++
	reconciliation.LastAttemptAt = &now
	logReconciliationTransition(ctx, slog.LevelInfo, "claimed reconciliation attempt", &reconciliation, domain.ReconciliationStatusProcessing)
	return &reconciliation, nil
}

// MarkActive records that Academic acknowledged the job. A row this statement did not
// change is not a transition, so a repeated confirmation cannot log a second
// completion for the same job. The read is best-effort: it exists only to carry the
// correlation ids into the log line, and the update keeps its original contract of
// succeeding even when no row matches.
func (r *paymentReconciliationRepository) MarkActive(ctx context.Context, id uuid.UUID, completedAt time.Time) error {
	var current domain.PaymentReconciliation
	_ = GetDB(ctx, r.db).First(&current, "id = ?", id).Error
	result := GetDB(ctx, r.db).Model(&domain.PaymentReconciliation{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":          domain.ReconciliationStatusActive,
		"completed_at":    completedAt,
		"next_attempt_at": nil,
		"last_error":      nil,
	})
	if result.Error != nil {
		return result.Error
	}
	current.ID = id
	current.Status = domain.ReconciliationStatusActive
	current.LastError = nil
	logReconciliationTransition(ctx, slog.LevelInfo, "completed enrollment reconciliation", &current, domain.ReconciliationStatusActive, "completed_at", completedAt)
	return nil
}

// MarkRetry records the outcome of a failed attempt. The attempt count the statement is
// compared against is the one ClaimDue already incremented, so the row is read first;
// that same read supplies the correlation ids for the log line. When no attempt limit is
// configured the read is best-effort, because the original contract updated the row
// without requiring it to exist and that behaviour is preserved.
func (r *paymentReconciliationRepository) MarkRetry(ctx context.Context, id uuid.UUID, nextAttemptAt time.Time, lastError string, maxAttempts int) error {
	status := domain.ReconciliationStatusPending
	var current domain.PaymentReconciliation
	if maxAttempts > 0 {
		if err := GetDB(ctx, r.db).First(&current, "id = ?", id).Error; err != nil {
			return err
		}
		if current.AttemptCount >= maxAttempts {
			status = domain.ReconciliationStatusTerminalFailed
			nextAttemptAt = time.Time{}
		}
	} else {
		_ = GetDB(ctx, r.db).First(&current, "id = ?", id).Error
	}
	var nextAttempt interface{} = nextAttemptAt
	if nextAttemptAt.IsZero() {
		nextAttempt = nil
	}
	result := GetDB(ctx, r.db).Model(&domain.PaymentReconciliation{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":          status,
		"next_attempt_at": nextAttempt,
		"last_error":      lastError,
	})
	if result.Error != nil {
		return result.Error
	}
	current.ID = id
	current.Status = status
	current.LastError = &lastError
	if status == domain.ReconciliationStatusTerminalFailed {
		// Operator-visible by design: a job that will never be retried again is exactly
		// what the reconciliation list endpoint exists to find.
		logReconciliationTransition(ctx, slog.LevelError, "enrollment reconciliation reached the attempt limit", &current, status, "error", lastError, "max_attempts", maxAttempts)
	} else {
		logReconciliationTransition(ctx, slog.LevelWarn, "enrollment reconciliation retry scheduled", &current, status, "error", lastError, "next_attempt_at", nextAttemptAt)
	}
	return nil
}

const reconciliationListLimit = 200

// ListByStatus returns the reconciliation rows an operator asked for, newest first so a
// freshly failed job is the first thing on the page. `status` is validated by the caller
// against the domain set, and an empty status lists every row.
func (r *paymentReconciliationRepository) ListByStatus(ctx context.Context, status string, limit int) ([]domain.PaymentReconciliation, error) {
	if limit <= 0 || limit > reconciliationListLimit {
		limit = reconciliationListLimit
	}
	var reconciliations []domain.PaymentReconciliation
	query := GetDB(ctx, r.db).Order("created_at DESC").Limit(limit)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if err := query.Find(&reconciliations).Error; err != nil {
		return nil, err
	}
	return reconciliations, nil
}

// RequeueTerminalFailed re-drives jobs that ran out of attempts. The status guard is the
// whole contract: only a `terminal_failed` row is moved back to `pending`, so replaying
// this call cannot reset a job that is already pending, in flight, or completed, and the
// attempt count is preserved so the configured limit still bounds the next round. The
// pending timestamp is set to the supplied instant rather than cleared, so the job is due
// immediately instead of waiting on the lease sweep.
func (r *paymentReconciliationRepository) RequeueTerminalFailed(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	result := GetDB(ctx, r.db).Model(&domain.PaymentReconciliation{}).
		Where("id IN (?)", GetDB(ctx, r.db).Model(&domain.PaymentReconciliation{}).
			Select("id").
			Where("status = ?", domain.ReconciliationStatusTerminalFailed).
			Order("created_at ASC").
			Limit(limit)).
		Updates(map[string]interface{}{
			"status":          domain.ReconciliationStatusPending,
			"next_attempt_at": now,
			"updated_at":      now,
		})
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected > 0 {
		slog.InfoContext(ctx, "requeued terminal reconciliation jobs", "count", result.RowsAffected)
	}
	return result.RowsAffected, nil
}
