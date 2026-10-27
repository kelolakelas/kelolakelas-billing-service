package repository_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/academic"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/duitku"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type initialFeePolicy struct{}

func (initialFeePolicy) AppliedPlatformFeePolicy(context.Context) (domain.PlatformFeePolicy, error) {
	return domain.PlatformFeePolicy{}, nil
}

type initialClock struct{ now time.Time }

func (c initialClock) Now() time.Time { return c.now }

// Uses real migrations, row locks, outbox, HTTP clients and worker restarts.
// The provider and Academic HTTP peers are controlled fixtures; the real-service
// sandbox journey is recorded separately in the acceptance evidence.
func TestInitialPurchaseReconciliationPostgres(t *testing.T) {
	dsn := os.Getenv("KEL221_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL221_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	dir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New((&url.URL{Scheme: "file", Path: dir}).String(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatal(err)
	}
	m.Close()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	ctx := context.Background()
	var academicStatus atomic.Int64
	academicStatus.Store(http.StatusOK)
	var calls atomic.Int64
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Service-Credential") != "fixture-internal" {
			w.WriteHeader(401)
			return
		}
		calls.Add(1)
		w.WriteHeader(int(academicStatus.Load()))
	}))
	defer peer.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2/inquiry" {
			json.NewEncoder(w).Encode(map[string]interface{}{"statusCode": "00", "reference": "ref-" + p["merchantOrderId"].(string), "paymentUrl": "https://sandbox.example.test/pay"})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"statusCode": "00", "merchantOrderId": p["merchantOrderId"], "reference": "ref-" + p["merchantOrderId"].(string), "amount": "40000"})
	}))
	defer provider.Close()
	ac := academic.NewClient(peer.URL, "fixture-internal")
	gateway := duitku.NewClient(provider.URL, "fixture-key", "fixture-merchant", nil)
	cfg := config.Config{PaymentReconciliationMaxAttempts: 2}
	recon := repository.NewPaymentReconciliationRepository(db)
	txs := repository.NewTransactionRepository(db)
	u := usecase.WithPlatformFeePolicy(usecase.NewTransactionUsecaseWithReconciliation(txs, repository.NewWalletRepository(db), repository.NewLedgerEntryRepository(db), repository.NewSubscriptionRepository(db), gateway, ac, cfg, repository.NewTransactionManager(db), recon), initialFeePolicy{})
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "group", true: "private"}[private], func(t *testing.T) {
			req := &domain.GenerateSubscriptionPaymentRequest{EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), BillingCycle: "monthly", SubtotalAmount: 40000, PrivateScheduleRequest: private}
			payment, err := u.GenerateSubscriptionPayment(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			payload := &domain.DuitkuCallbackPayload{MerchantCode: "fixture-merchant", MerchantOrderID: payment.TransactionID.String(), Amount: "40000", ResultCode: "00", Reference: payment.PaymentIntentID}
			academicStatus.Store(503)
			if err := u.HandleDuitkuWebhook(ctx, payload); err != nil {
				t.Fatal(err)
			}
			job, err := recon.GetByTransactionID(ctx, payment.TransactionID)
			if err != nil || job.Status != "pending" || job.AttemptCount != 1 {
				t.Fatalf("owed activation missing: job=%+v err=%v", job, err)
			}
			// New worker instance represents a process restart. Advance its clock
			// rather than sleeping through the configured retry backoff.
			now := time.Now().UTC().Add(time.Hour)
			usecase.NewPaymentReconciliationWorker(recon, ac, cfg, initialClock{now}).RunOnce(ctx)
			job, err = recon.GetByTransactionID(ctx, payment.TransactionID)
			if err != nil || job.Status != "terminal_failed" || job.LastError == nil {
				t.Fatalf("terminal diagnostic missing: %+v %v", job, err)
			}
			if n, err := recon.RequeueTerminalFailed(ctx, now, 100); err != nil || n != 1 {
				t.Fatalf("re-drive=%d %v", n, err)
			}
			academicStatus.Store(200)
			usecase.NewPaymentReconciliationWorker(recon, ac, cfg, initialClock{now}).RunOnce(ctx)
			job, err = recon.GetByTransactionID(ctx, payment.TransactionID)
			if err != nil || job.Status != "active" {
				t.Fatalf("recovery=%+v %v", job, err)
			}
			before := calls.Load()
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := u.HandleDuitkuWebhook(ctx, payload); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if calls.Load() != before {
				t.Fatal("paid replay dispatched a second activation")
			}
			var count int64
			db.Model(&domain.PaymentReconciliation{}).Where("transaction_id = ?", payment.TransactionID).Count(&count)
			if count != 1 {
				t.Fatalf("outbox rows=%d", count)
			}
			// This fixture settles non-sandbox funds to verify locks/uniqueness;
			// it uses only the disposable database, never a real tenant wallet.
			db.Model(&domain.LedgerEntry{}).Where("reference_id = ?", payment.TransactionID).Count(&count)
			if count != 1 {
				t.Fatalf("ledger entries=%d", count)
			}
			var wallet domain.Wallet
			if err := db.Where("tenant_id = ?", req.TenantID).First(&wallet).Error; err != nil || wallet.AvailableBalance != 40000 {
				t.Fatalf("wallet=%+v err=%v", wallet, err)
			}
			// An explicit Academic rejection also remains diagnostic/retriable.
			academicStatus.Store(409)
			db.Model(job).Updates(map[string]interface{}{"status": "pending", "next_attempt_at": now, "attempt_count": 1})
			usecase.NewPaymentReconciliationWorker(recon, ac, cfg, initialClock{now}).RunOnce(ctx)
			job, _ = recon.GetByTransactionID(ctx, payment.TransactionID)
			if job.Status != "terminal_failed" {
				t.Fatalf("terminal enrollment rejection was swallowed: %+v", job)
			}
			academicStatus.Store(200)
			if err := db.Delete(job).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("concurrent first settlement", func(t *testing.T) {
		req := &domain.GenerateSubscriptionPaymentRequest{EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), BillingCycle: "monthly", SubtotalAmount: 40000, PrivateScheduleRequest: true}
		payment, err := u.GenerateSubscriptionPayment(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		payload := &domain.DuitkuCallbackPayload{MerchantOrderID: payment.TransactionID.String(), Amount: "40000", ResultCode: "00", Reference: payment.PaymentIntentID}
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := u.HandleDuitkuWebhook(ctx, payload); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		var count int64
		if err := db.Model(&domain.LedgerEntry{}).Where("reference_id = ?", payment.TransactionID).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("concurrent ledger=%d err=%v", count, err)
		}
		var wallet domain.Wallet
		if err := db.Where("tenant_id = ?", req.TenantID).First(&wallet).Error; err != nil || wallet.AvailableBalance != 40000 {
			t.Fatalf("concurrent wallet=%+v err=%v", wallet, err)
		}
		job, err := recon.GetByTransactionID(ctx, payment.TransactionID)
		if err != nil || job.Status != "active" {
			t.Fatalf("concurrent activation=%+v err=%v", job, err)
		}
	})
	// Initial expiry must enqueue a release despite its subscription/period;
	// a renewal expiry must keep the existing seat untouched.
	for _, renewal := range []bool{false, true} {
		id, enrollment, sub, period := uuid.New(), uuid.New(), uuid.New(), time.Now().UTC()
		if !renewal {
			id = enrollment
		}
		if err := db.Create(&domain.Subscription{ID: sub, EnrollmentID: enrollment, TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), BillingCycle: "monthly", NextBillingDate: period.AddDate(0, 1, 0), Status: "active"}).Error; err != nil {
			t.Fatal(err)
		}
		row := &domain.Transaction{ID: id, MerchantOrderID: id.String(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), EnrollmentID: enrollment, SubscriptionID: &sub, BillingPeriodStart: &period, Status: "pending", GrossAmount: 40000, SubtotalAmount: 40000, NetAmount: 40000, Currency: "IDR", InvoiceExpiresAt: &period}
		t.Cleanup(func() { db.Where("transaction_id = ?", id).Delete(&domain.PaymentReconciliation{}) })
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := txs.(repository.TransactionExpiryRepository).ExpireDue(ctx, period.Add(time.Minute), 100); err != nil {
			t.Fatal(err)
		}
		job, err := recon.GetByTransactionID(ctx, id)
		if renewal {
			if err != gorm.ErrRecordNotFound {
				t.Fatalf("renewal release=%+v err=%v", job, err)
			}
		} else if err != nil || job.Kind != "release" {
			t.Fatalf("initial release=%+v err=%v", job, err)
		}
	}
}
