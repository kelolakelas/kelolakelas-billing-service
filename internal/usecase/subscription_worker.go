package usecase

import (
	"context"
	"errors"
	"fmt"
	"html"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type SubscriptionWorker struct {
	subscriptions repository.SubscriptionRepository
	transactions  repository.TransactionRepository
	gateway       domain.PaymentGateway
	email         domain.EmailClient
	cfg           config.Config
	clock         Clock
}

func NewSubscriptionWorker(subscriptions repository.SubscriptionRepository, transactions repository.TransactionRepository, gateway domain.PaymentGateway, email domain.EmailClient, cfg config.Config, clock ...Clock) *SubscriptionWorker {
	c := Clock(realClock{})
	if len(clock) > 0 && clock[0] != nil {
		c = clock[0]
	}
	return &SubscriptionWorker{subscriptions: subscriptions, transactions: transactions, gateway: gateway, email: email, cfg: cfg, clock: c}
}

func (w *SubscriptionWorker) Run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	interval := time.Duration(w.cfg.SubscriptionWorkerIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	w.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

func (w *SubscriptionWorker) RunOnce(ctx context.Context) {
	now := w.clock.Now()
	subscriptions, err := w.subscriptions.ListDueForRenewal(ctx, now.AddDate(0, 0, 7))
	if err != nil {
		return
	}
	for i := range subscriptions {
		if ctx.Err() != nil {
			return
		}
		_ = w.process(ctx, &subscriptions[i], now)
	}
}

func (w *SubscriptionWorker) process(ctx context.Context, subscription *domain.Subscription, now time.Time) error {
	period := dateOnly(subscription.NextBillingDate)
	if now.After(period.AddDate(0, 0, 7)) {
		return nil
	}
	tx, err := w.transactions.GetBySubscriptionPeriod(ctx, subscription.ID, period)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		base, baseErr := w.transactions.GetByEnrollmentID(ctx, subscription.EnrollmentID)
		if baseErr != nil {
			return baseErr
		}
		merchantOrderID := "renewal-" + uuid.NewString()
		tx = &domain.Transaction{ID: uuid.New(), MerchantOrderID: merchantOrderID, TenantID: base.TenantID, ParentID: base.ParentID, StudentID: base.StudentID, EnrollmentID: base.EnrollmentID, SubtotalAmount: base.SubtotalAmount, DiscountAmount: base.DiscountAmount, GrossAmount: base.GrossAmount, PlatformFee: base.PlatformFee, PaymentGatewayFee: base.PaymentGatewayFee, NetAmount: base.NetAmount, SubscriptionID: &subscription.ID, BillingPeriodStart: &period, BillingEmail: subscription.BillingEmail, Currency: base.Currency, Status: "pending", IsSandbox: base.IsSandbox, PaymentGatewayProvider: base.PaymentGatewayProvider}
		if err = w.transactions.Create(ctx, tx); err != nil {
			tx, err = w.transactions.GetBySubscriptionPeriod(ctx, subscription.ID, period)
			if err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	if tx.Status == "paid" {
		return nil
	}
	if tx.CheckoutSessionURL == nil || *tx.CheckoutSessionURL == "" || tx.Status == domain.TransactionStatusFailed || tx.Status == domain.TransactionStatusExpired {
		if locked, ok := w.transactions.(repository.TransactionLockingRepository); ok {
			// The renewal invoice takes the same exclusive claim as the direct payment
			// path, so a renewal that is already being issued (or one abandoned by a dead
			// process, once the claim timeout has passed) is never invoiced twice.
			claimed, claimErr := locked.ClaimInvoice(ctx, tx.ID, now, w.claimTimeoutMinutes())
			if claimErr != nil {
				return claimErr
			}
			if !claimed {
				// Another caller owns the invoice creation for this period; leaving it
				// alone is what keeps exactly one invoice per billing period.
				return nil
			}
		} else if tx.Status == domain.TransactionStatusFailed || tx.Status == domain.TransactionStatusExpired {
			tx.Status = domain.TransactionStatusPending
			tx.CheckoutSessionURL = nil
			tx.PaymentIntentID = nil
			tx.InvoiceExpiresAt = nil
			tx.ExpiredAt = nil
			if err := w.transactions.Update(ctx, tx); err != nil {
				return err
			}
		}
		validityMinutes := w.cfg.SubscriptionPaymentExpiryPeriodDays * 24 * 60
		if validityMinutes <= 0 {
			validityMinutes = domain.DefaultInvoiceValidityMinutes
		}
		invoice, invoiceErr := w.gateway.CreateInvoice(ctx, &domain.CreateInvoiceRequest{MerchantOrderID: tx.MerchantOrderID, Amount: tx.GrossAmount, ProductDetails: subscription.ClassName, Email: tx.BillingEmail, PaymentMethod: "VC", CallbackURL: w.cfg.DuitkuCallbackURL, ReturnURL: w.cfg.DuitkuReturnURL, ExpiryPeriod: validityMinutes})
		if invoiceErr != nil {
			// The renewal claim is released on the same path that took it, so a failed
			// renewal does not park the period in `creating` forever. The update only
			// matches the row this worker still owns, so a link that was stored in the
			// meantime is preserved.
			releaseFailedInvoiceClaim(ctx, w.transactions, tx.ID, invoiceErr, now)
			return invoiceErr
		}
		if _, err := w.markInvoiceIssued(ctx, tx, invoice, domain.InvoiceExpiresAt(now, validityMinutes), now); err != nil {
			return err
		}
	}
	return w.sendEmails(ctx, tx, subscription, now)
}

// claimTimeoutMinutes is how long a renewal invoice claim may stay unowned before it
// is considered abandoned. It mirrors the direct payment path so both ways of issuing
// an invoice recover from a crash after the same delay.
func (w *SubscriptionWorker) claimTimeoutMinutes() int {
	if w.cfg.TransactionClaimTimeoutMinutes <= 0 {
		return domain.DefaultTransactionClaimTimeoutMinutes
	}
	return w.cfg.TransactionClaimTimeoutMinutes
}

// markInvoiceIssued stores the renewal payment link through the same conditional
// update used by the direct payment path, so a cancellation that won the race is
// never overwritten and a paid transaction is never moved back to awaiting payment.
func (w *SubscriptionWorker) markInvoiceIssued(ctx context.Context, tx *domain.Transaction, invoice *domain.CreateInvoiceResponse, expiresAt, now time.Time) (bool, error) {
	if invoice == nil {
		return false, fmt.Errorf("invoice response is missing")
	}
	if locked, ok := w.transactions.(repository.TransactionLockingRepository); ok {
		issued, err := locked.MarkInvoiceIssued(ctx, tx.ID, invoice.PaymentURL, invoice.Reference, expiresAt)
		if err != nil {
			return false, err
		}
		if !issued {
			return false, nil
		}
		tx.Status = domain.TransactionStatusPending
		tx.CheckoutSessionURL = &invoice.PaymentURL
		tx.PaymentIntentID = &invoice.Reference
		tx.InvoiceExpiresAt = &expiresAt
		tx.InvoiceClaimedAt = nil
		tx.InvoiceFailureReason = nil
		return true, nil
	}
	tx.PaymentIntentID = &invoice.Reference
	tx.CheckoutSessionURL = &invoice.PaymentURL
	tx.InvoiceExpiresAt = &expiresAt
	tx.Status = domain.TransactionStatusPending
	tx.InvoiceClaimedAt = nil
	tx.InvoiceFailureReason = nil
	if err := w.transactions.Update(ctx, tx); err != nil {
		return false, err
	}
	return true, nil
}

func (w *SubscriptionWorker) sendEmails(ctx context.Context, tx *domain.Transaction, sub *domain.Subscription, now time.Time) error {
	if w.email == nil || tx.BillingEmail == "" || tx.CheckoutSessionURL == nil {
		return nil
	}
	link := *tx.CheckoutSessionURL
	send := func(subject string) error {
		body := fmt.Sprintf("Hello %s,<br>Tagihan %s untuk periode %s sebesar IDR %d jatuh tempo %s.<br><a href=\"%s\">Bayar sekarang</a><br>Reference: %s<br>Email reminder ini dapat diabaikan jika pembayaran sudah dilakukan.", html.EscapeString(sub.ParentName), html.EscapeString(sub.ClassName), dateOnlyString(tx.BillingPeriodStart), tx.GrossAmount, sub.NextBillingDate.Format("2006-01-02"), html.EscapeString(link), html.EscapeString(tx.MerchantOrderID))
		return w.email.Send(ctx, domain.EmailMessage{To: tx.BillingEmail, Subject: subject, HTML: body})
	}
	if tx.PaymentLinkSentAt == nil {
		if locked, ok := w.transactions.(repository.TransactionLockingRepository); ok {
			claimed, err := locked.ClaimPaymentLinkEmail(ctx, tx.ID, now)
			if err != nil || !claimed {
				return err
			}
			if err := send("Payment link subscription"); err != nil {
				tx.PaymentLinkSentAt = nil
				_ = w.transactions.Update(ctx, tx)
				return err
			}
		} else if err := send("Payment link subscription"); err != nil {
			return err
		}
		return nil
	}
	if tx.LastReminderSentAt == nil || now.Sub(*tx.LastReminderSentAt) >= time.Duration(w.cfg.SubscriptionPaymentReminderIntervalDays)*24*time.Hour {
		if locked, ok := w.transactions.(repository.TransactionLockingRepository); ok {
			claimed, err := locked.ClaimReminderEmail(ctx, tx.ID, now, w.cfg.SubscriptionPaymentReminderIntervalDays)
			if err != nil || !claimed {
				return err
			}
			return send("Payment reminder subscription")
		}
	}
	return nil
}

func dateOnly(value time.Time) time.Time {
	y, m, d := value.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, value.Location())
}
func dateOnlyString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return dateOnly(*value).Format("2006-01-02")
}
