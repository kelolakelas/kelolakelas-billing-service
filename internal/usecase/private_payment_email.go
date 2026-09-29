package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"log/slog"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

const privateEmailClaimTimeout = 10 * time.Minute

// RunPrivatePaymentEmails retries persisted private invoices independently of
// approval requests. Claim age bounds recovery after process death.
func RunPrivatePaymentEmails(ctx context.Context, transactions repository.TransactionRepository, sender domain.EmailClient, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if repo, ok := transactions.(repository.PrivatePaymentEmailRepository); ok && sender != nil {
			rows, err := repo.ListPrivatePaymentEmails(ctx, time.Now(), 100)
			if err != nil {
				slog.ErrorContext(ctx, "list private payment emails failed", "error", err)
			} else {
				for _, tx := range rows {
					if ctx.Err() != nil {
						return
					}
					dispatchPrivatePaymentEmail(ctx, transactions, sender, tx.ID)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (u *transactionUsecase) dispatchPrivatePaymentEmail(ctx context.Context, id uuid.UUID) {
	dispatchPrivatePaymentEmail(ctx, u.txRepo, u.outcomeEmail, id)
}

// dispatchPrivatePaymentEmail uses only the persisted transaction, not the
// caller's current sender_email. A claim prevents concurrent sends; Resend's
// 24-hour idempotency key covers a process dying after acceptance but before
// acknowledgement. Beyond 24 hours such a crash can cause a duplicate.
func dispatchPrivatePaymentEmail(ctx context.Context, transactions repository.TransactionRepository, sender domain.EmailClient, id uuid.UUID) {
	if sender == nil {
		return
	}
	repo, ok := transactions.(repository.PrivatePaymentEmailRepository)
	if !ok {
		return
	}
	tx, err := transactions.GetByID(ctx, id)
	if err != nil || !validPrivatePaymentEmail(tx, time.Now()) {
		return
	}
	claimedAt := time.Now().UTC().Round(time.Microsecond)
	claimed, err := repo.ClaimPrivatePaymentEmail(ctx, id, claimedAt, privateEmailClaimTimeout)
	if err != nil {
		slog.ErrorContext(ctx, "claim private payment email failed", "transaction_id", id, "error", err)
		return
	}
	if !claimed {
		return
	}
	// Re-read after claiming: settlement or a reissued invoice can change the
	// transaction while a dispatcher is waiting for the row lock.
	tx, err = transactions.GetByID(ctx, id)
	if err != nil || !validPrivatePaymentEmail(tx, time.Now()) {
		if _, finishErr := repo.FinishPrivatePaymentEmail(ctx, id, claimedAt, "invoice no longer payable"); finishErr != nil {
			slog.ErrorContext(ctx, "release private payment email claim failed", "transaction_id", id, "error", finishErr)
		}
		return
	}
	body := fmt.Sprintf("Tagihan kelas private sebesar IDR %d berlaku hingga %s.<br><a href=\"%s\">Bayar sekarang</a>", tx.GrossAmount, tx.InvoiceExpiresAt.UTC().Format("2006-01-02 15:04 MST"), html.EscapeString(*tx.CheckoutSessionURL))
	// The transaction ID stays stable on invoice reissue; the payment intent
	// distinguishes the replacement so a stale provider key cannot suppress it.
	digest := sha256.Sum256([]byte(valueOrEmpty(tx.PaymentIntentID)))
	key := "private-payment/" + id.String() + "/" + hex.EncodeToString(digest[:])
	sendErr := sender.Send(ctx, domain.EmailMessage{To: tx.BillingEmail, Subject: "Link pembayaran kelas private", HTML: body, IdempotencyKey: key})
	failure := ""
	if sendErr != nil {
		failure = sendErr.Error()
		slog.WarnContext(ctx, "private payment email delivery failed", "transaction_id", id, "error", sendErr)
	}
	// Do not use a cancelled HTTP request context to release the claim.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := repo.FinishPrivatePaymentEmail(finishCtx, id, claimedAt, failure); err != nil {
		slog.ErrorContext(ctx, "finish private payment email failed", "transaction_id", id, "error", err)
	}
}

func validPrivatePaymentEmail(tx *domain.Transaction, now time.Time) bool {
	if tx == nil || !tx.PrivateScheduleRequest || tx.Status != domain.TransactionStatusPending || tx.PrivatePaymentEmailSentAt != nil || tx.InvoiceExpiresAt == nil || !tx.InvoiceExpiresAt.After(now) || tx.CheckoutSessionURL == nil || tx.PaymentIntentID == nil || *tx.PaymentIntentID == "" {
		return false
	}
	address, err := mail.ParseAddress(tx.BillingEmail)
	if err != nil || address.Address != tx.BillingEmail || strings.ContainsAny(tx.BillingEmail, "\r\n") {
		return false
	}
	link, err := url.Parse(*tx.CheckoutSessionURL)
	return err == nil && link.Scheme == "https" && link.Host != "" && link.User == nil && link.Fragment == ""
}
