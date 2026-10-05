package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type kel162Policy struct{}

func (kel162Policy) AppliedPlatformFeePolicy(context.Context) (domain.PlatformFeePolicy, error) {
	return domain.PlatformFeePolicy{Version: 1, PercentBps: 500, FixedFee: 10}, nil
}

type kel162Gateway struct {
	domain.PaymentGateway
	server *httptest.Server
	db     *gorm.DB
}

func (g kel162Gateway) CreateInvoice(ctx context.Context, r *domain.CreateInvoiceRequest) (*domain.CreateInvoiceResponse, error) {
	request, _ := http.NewRequestWithContext(ctx, "POST", g.server.URL, nil)
	resp, err := g.server.Client().Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("test provider failed")
	}
	return &domain.CreateInvoiceResponse{Reference: r.MerchantOrderID, PaymentURL: "https://test.invalid/pay"}, nil
}
func (g kel162Gateway) ValidateCallbackSignature(*domain.DuitkuCallbackPayload) bool { return true }
func (g kel162Gateway) TransactionStatus(ctx context.Context, id string) (*domain.PaymentStatus, error) {
	request, _ := http.NewRequestWithContext(ctx, "GET", g.server.URL, nil)
	resp, err := g.server.Client().Do(request)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	var tx domain.Transaction
	if err := g.db.First(&tx, "merchant_order_id = ?", id).Error; err != nil {
		return nil, err
	}
	return &domain.PaymentStatus{MerchantOrderID: id, Reference: id, Amount: tx.GrossAmount, StatusCode: domain.ResultCodeSuccess}, nil
}
func openKEL162Database(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("KEL162_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL162_TEST_DATABASE_URL")
	}
	t.Setenv("KEL161_TEST_DATABASE_URL", dsn)
	return openKEL161Database(t)
}
func kel162Service(t *testing.T, db *gorm.DB, fail bool) usecase.TransactionUsecase {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(502)
		} else {
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}
	}))
	t.Cleanup(server.Close)
	u := usecase.NewTransactionUsecase(repository.NewTransactionRepository(db), nil, nil, repository.NewSubscriptionRepository(db), nil, nil, config.Config{}, repository.NewTransactionManager(db))
	u = usecase.NewTransactionUsecase(repository.NewTransactionRepository(db), nil, nil, repository.NewSubscriptionRepository(db), kel162Gateway{server: server, db: db}, nil, config.Config{DuitkuAPIBaseURL: "https://sandbox.invalid"}, repository.NewTransactionManager(db))
	return usecase.WithVoucherReservations(usecase.WithPlatformFeePolicy(u, kel162Policy{}), repository.NewVoucherReservation(db))
}
func kel162Request(v domain.Voucher) *domain.GenerateSubscriptionPaymentRequest {
	return &domain.GenerateSubscriptionPaymentRequest{TenantID: v.TenantID, EnrollmentID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), ClassID: uuid.New(), BillingCycle: "monthly", SubtotalAmount: 10003, VoucherCode: v.Code, PaymentGatewayFee: 3}
}
func cleanupKEL162(t *testing.T, db *gorm.DB, tenant uuid.UUID) {
	t.Cleanup(func() {
		db.Exec("DELETE FROM payment_reconciliations WHERE transaction_id IN (SELECT id FROM transactions WHERE tenant_id = ?)", tenant)
		db.Unscoped().Where("tenant_id = ?", tenant).Delete(&domain.Transaction{})
		db.Unscoped().Where("tenant_id = ?", tenant).Delete(&domain.Subscription{})
	})
}
func kel162Uses(t *testing.T, db *gorm.DB, id uuid.UUID) int {
	t.Helper()
	var v domain.Voucher
	if err := db.First(&v, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return v.CurrentUses
}
func TestKEL162ConcurrentLastUse(t *testing.T) {
	db := openKEL162Database(t)
	v := seedKEL161Voucher(t, db, uuid.New(), "LAST", 0)
	max := 1
	db.Model(&v).Update("max_uses", max)
	cleanupKEL162(t, db, v.TenantID)
	u := kel162Service(t, db, false)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := u.GenerateSubscriptionPayment(context.Background(), kel162Request(v))
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, domain.ErrVoucherRejected) {
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 || kel162Uses(t, db, v.ID) != 1 {
		t.Fatalf("winners=%d uses=%d", winners.Load(), kel162Uses(t, db, v.ID))
	}
}
func TestKEL162ExpiryLatePaidOverCapAndReinvoice(t *testing.T) {
	db := openKEL162Database(t)
	v := seedKEL161Voucher(t, db, uuid.New(), "LATE", 0)
	db.Model(&v).Update("max_uses", 1)
	cleanupKEL162(t, db, v.TenantID)
	u := kel162Service(t, db, false)
	req := kel162Request(v)
	response, err := u.GenerateSubscriptionPayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if response.GrossAmount != 9003 {
		t.Fatalf("gross=%d", response.GrossAmount)
	}
	repo := repository.NewTransactionRepository(db)
	tx, err := repo.GetByID(context.Background(), req.EnrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if tx.SubscriptionID == nil {
		t.Fatal("initial invoice lost subscription association")
	}
	if tx.DiscountAmount != 1000 || tx.PlatformFee != 460 || tx.NetAmount != 8540 {
		t.Fatalf("bad fees %+v", tx)
	}
	past := time.Now().UTC().Add(-time.Hour)
	db.Model(tx).Update("invoice_expires_at", past)
	expiry := repo.(repository.TransactionExpiryRepository)
	if n, err := expiry.ExpireDue(context.Background(), time.Now(), 100); err != nil || n < 1 {
		t.Fatalf("expiry n=%d err=%v", n, err)
	}
	if kel162Uses(t, db, v.ID) != 0 {
		t.Fatal("expiry did not release")
	}
	second := kel162Request(v)
	if _, err := u.GenerateSubscriptionPayment(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	locking := repo.(repository.TransactionLockingRepository)
	if ok, err := locking.ClaimReinvoice(context.Background(), tx.ID, time.Now(), 10); ok || !errors.Is(err, domain.ErrVoucherRejected) {
		t.Fatalf("exhausted claim=%v err=%v", ok, err)
	}
	// A confirmed late payment keeps the original snapshot and re-takes over cap.
	tx, err = repo.GetByID(context.Background(), tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload := &domain.DuitkuCallbackPayload{MerchantOrderID: tx.MerchantOrderID, Amount: "9003", Reference: tx.MerchantOrderID, ResultCode: domain.ResultCodeSuccess}
	if err := u.HandleDuitkuWebhook(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if kel162Uses(t, db, v.ID) != 2 {
		t.Fatal("late paid did not retake over cap")
	}
	paid, err := repo.GetByID(context.Background(), tx.ID)
	if err != nil || paid.Status != "paid" || paid.GrossAmount != 9003 || paid.DiscountAmount != 1000 || paid.VoucherUseClaimedAt == nil {
		t.Fatalf("late payment snapshot/retake: %+v %v", paid, err)
	}
	var subscription domain.Subscription
	if err := db.First(&subscription, "id = ?", *paid.SubscriptionID).Error; err != nil || subscription.Status != "active" {
		t.Fatalf("subscription not activated: %+v %v", subscription, err)
	}
	if err := u.HandleDuitkuWebhook(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if kel162Uses(t, db, v.ID) != 2 {
		t.Fatal("paid replay retook twice")
	}
	// Another released snapshot is refused while inactive; then re-reserves once.
	secondTx, _ := repo.GetByID(context.Background(), second.EnrollmentID)
	if ok, err := locking.CancelUnpaid(context.Background(), secondTx.ID); !ok || err != nil {
		t.Fatal(err)
	}
	// cancelled is intentionally not reinvoiceable: use failed status for a recoverable invoice.
	db.Model(secondTx).Updates(map[string]interface{}{"status": "failed", "checkout_session_url": nil})
	db.Model(&v).Updates(map[string]interface{}{"is_active": false, "max_uses": 3})
	if ok, err := locking.ClaimReinvoice(context.Background(), secondTx.ID, time.Now(), 10); ok || !errors.Is(err, domain.ErrVoucherRejected) {
		t.Fatalf("inactive claim=%v %v", ok, err)
	}
	db.Model(&v).Update("is_active", true)
	if ok, err := locking.ClaimReinvoice(context.Background(), secondTx.ID, time.Now(), 10); !ok || err != nil {
		t.Fatalf("available claim=%v %v", ok, err)
	}
	if ok, err := locking.ClaimReinvoice(context.Background(), secondTx.ID, time.Now(), 10); ok || err != nil {
		t.Fatalf("duplicate claim=%v %v", ok, err)
	}
	got, _ := repo.GetByID(context.Background(), secondTx.ID)
	if got.GrossAmount != 9003 || got.PlatformFee != 460 || got.DiscountAmount != 1000 || kel162Uses(t, db, v.ID) != 2 {
		t.Fatalf("snapshot/count changed %+v uses=%d", got, kel162Uses(t, db, v.ID))
	}
}
func TestKEL162ProviderFailureReleaseAndSpoofedAmountIgnored(t *testing.T) {
	db := openKEL162Database(t)
	v := seedKEL161Voucher(t, db, uuid.New(), "FAIL", 0)
	cleanupKEL162(t, db, v.TenantID)
	u := kel162Service(t, db, true)
	req := kel162Request(v)
	fake := uuid.New()
	req.VoucherID = &fake
	req.DiscountAmount = 9999
	if _, err := u.GenerateSubscriptionPayment(context.Background(), req); err == nil {
		t.Fatal("expected provider failure")
	}
	if kel162Uses(t, db, v.ID) != 0 {
		t.Fatal("failed claim did not release")
	}
	tx, err := repository.NewTransactionRepository(db).GetByID(context.Background(), req.EnrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Status != "failed" || tx.DiscountAmount != 1000 || tx.VoucherID == nil || *tx.VoucherID != v.ID {
		t.Fatalf("spoof accepted %+v", tx)
	}
}
func TestKEL162PreviewEligibilityReadOnly(t *testing.T) {
	db := openKEL162Database(t)
	v := seedKEL161Voucher(t, db, uuid.New(), "PREVIEW", 0)
	preview := usecase.NewVoucherPreviewUsecase(repository.NewVoucherReservation(db))
	request := usecase.VoucherPreviewRequest{TenantID: v.TenantID, VoucherCode: " preview ", SubtotalAmount: 10003}
	got, err := preview.Preview(context.Background(), request)
	if err != nil || got.DiscountAmount != 1000 || got.GrossAmount != 9003 {
		t.Fatalf("preview=%+v %v", got, err)
	}
	if kel162Uses(t, db, v.ID) != 0 {
		t.Fatal("preview reserved")
	}
	for _, change := range []string{"foreign", "minimum", "inactive", "future", "expired", "quota"} {
		t.Run(change, func(t *testing.T) {
			db.Model(&v).Updates(map[string]interface{}{"is_active": true, "min_transaction_amount": 0, "valid_from": nil, "valid_until": nil, "max_uses": nil})
			r := request
			switch change {
			case "foreign":
				r.TenantID = uuid.New()
			case "minimum":
				db.Model(&v).Update("min_transaction_amount", 20000)
			case "inactive":
				db.Model(&v).Update("is_active", false)
			case "future":
				db.Model(&v).Update("valid_from", time.Now().Add(time.Hour))
			case "expired":
				db.Model(&v).Update("valid_until", time.Now().UTC().Add(-time.Hour))
			case "quota":
				db.Model(&v).Update("max_uses", 0)
			}
			if _, err := preview.Preview(context.Background(), r); !errors.Is(err, domain.ErrVoucherRejected) {
				t.Fatal(fmt.Sprintf("expected rejected: %v", err))
			}
		})
	}
}
