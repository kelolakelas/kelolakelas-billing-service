package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func (r *transactionRepository) ListPrivatePaymentEmails(ctx context.Context, now time.Time, limit int) ([]domain.Transaction, error) {
	var rows []domain.Transaction
	err := r.getDB(ctx).Where(`private_schedule_request = true AND status = ? AND invoice_expires_at > ?
  AND checkout_session_url IS NOT NULL AND checkout_session_url <> '' AND billing_email <> ''
  AND private_payment_email_sent_at IS NULL
  AND (private_payment_email_claimed_at IS NULL OR private_payment_email_claimed_at < ?)`,
		domain.TransactionStatusPending, now, now.Add(-10*time.Minute)).Order("created_at, id").Limit(limit).Find(&rows).Error
	return rows, err
}

func (r *transactionRepository) ClaimPrivatePaymentEmail(ctx context.Context, id uuid.UUID, now time.Time, timeout time.Duration) (bool, error) {
	result := r.getDB(ctx).Model(&domain.Transaction{}).Where(`id = ? AND private_schedule_request = true AND status = ?
  AND invoice_expires_at > ? AND checkout_session_url IS NOT NULL AND checkout_session_url <> ''
  AND billing_email <> '' AND private_payment_email_sent_at IS NULL
  AND (private_payment_email_claimed_at IS NULL OR private_payment_email_claimed_at < ?)`,
		id, domain.TransactionStatusPending, now, now.Add(-timeout)).Updates(map[string]interface{}{
		"private_payment_email_claimed_at": now, "private_payment_email_failure_reason": nil,
	})
	return result.RowsAffected == 1, result.Error
}

func (r *transactionRepository) FinishPrivatePaymentEmail(ctx context.Context, id uuid.UUID, claimedAt time.Time, failure string) (bool, error) {
	updates := map[string]interface{}{"private_payment_email_claimed_at": nil}
	if failure == "" {
		updates["private_payment_email_sent_at"] = claimedAt
		updates["private_payment_email_failure_reason"] = nil
	} else {
		updates["private_payment_email_failure_reason"] = failure
	}
	result := r.getDB(ctx).Model(&domain.Transaction{}).
		Where("id = ? AND status = ? AND private_payment_email_claimed_at = ?", id, domain.TransactionStatusPending, claimedAt).
		Updates(updates)
	return result.RowsAffected == 1, result.Error
}
