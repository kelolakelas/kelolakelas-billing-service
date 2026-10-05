package repository_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestKEL152RefundPostgres(t *testing.T) {
	dsn := os.Getenv("KEL152_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL152_TEST_DATABASE_URL")
	}
	path, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New((&url.URL{Scheme: "file", Path: path}).String(), dsn)
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
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	ctx := context.Background()
	tenant, actor := uuid.New(), uuid.New()
	sub := domain.Subscription{ID: uuid.New(), EnrollmentID: uuid.New(), TenantID: tenant, Status: "active", BillingCycle: "monthly", NextBillingDate: time.Now()}
	if err = db.Create(&sub).Error; err != nil {
		t.Fatal(err)
	}
	tx := domain.Transaction{ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: tenant, ParentID: uuid.New(), StudentID: uuid.New(), EnrollmentID: sub.EnrollmentID, SubscriptionID: &sub.ID, Status: "paid", Currency: "IDR", GrossAmount: 1000}
	if err = db.Create(&tx).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{"DELETE FROM transaction_refunds WHERE transaction_id = ?", "DELETE FROM payment_reconciliations WHERE transaction_id = ?", "DELETE FROM transactions WHERE id = ?"} {
			if err := db.Exec(query, tx.ID).Error; err != nil {
				t.Error(err)
			}
		}
		if err := db.Delete(&sub).Error; err != nil {
			t.Error(err)
		}
	})
	repo := repository.NewRefundRepository(db)
	req := domain.RefundRequest{Reason: "manual refund", TransferReference: "transfer-001"}
	if _, err = repo.RecordRefund(ctx, uuid.New(), actor, tx.ID, req); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("foreign tenant: %v", err)
	}
	if _, err = repo.RecordRefund(ctx, tenant, actor, tx.ID, domain.RefundRequest{Reason: " "}); !errors.Is(err, repository.ErrInvalidRefund) {
		t.Fatalf("invalid: %v", err)
	}
	recon := repository.NewPaymentReconciliationRepository(db)
	job := domain.PaymentReconciliation{ID: uuid.New(), TransactionID: tx.ID, EnrollmentID: tx.EnrollmentID, Kind: domain.ReconciliationKindActivation, Status: domain.ReconciliationStatusPending}
	if err = recon.EnsureActivation(ctx, &job); err != nil {
		t.Fatal(err)
	}
	claimed, err := recon.ClaimDue(ctx, tx.ID, time.Now(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	activationDone := make(chan error, 1)
	go func() {
		activationDone <- recon.GuardActivation(ctx, tx.ID, func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	refundDone := make(chan error, 1)
	go func() { _, e := repo.RecordRefund(ctx, tenant, actor, tx.ID, req); refundDone <- e }()
	select {
	case e := <-refundDone:
		t.Fatalf("refund committed during activation: %v", e)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err = <-activationDone; err != nil {
		t.Fatal(err)
	}
	if err = <-refundDone; err != nil {
		t.Fatal(err)
	}
	called := false
	if err = recon.GuardActivation(ctx, claimed.TransactionID, func(context.Context) error { called = true; return nil }); err != nil || called {
		t.Fatalf("activation after refund: %v called=%v", err, called)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := repo.RecordRefund(ctx, tenant, actor, tx.ID, req); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var count int64
	db.Model(&domain.TransactionRefund{}).Where("transaction_id = ?", tx.ID).Count(&count)
	if count != 1 {
		t.Fatalf("refund rows=%d", count)
	}
	var stored domain.TransactionRefund
	db.First(&stored, "transaction_id = ?", tx.ID)
	if stored.ActorID != actor || stored.TransferReference != req.TransferReference {
		t.Fatalf("record overwritten: %+v", stored)
	}
	db.First(&sub, "id = ?", sub.ID)
	if sub.Status != "cancelled" {
		t.Fatalf("subscription %s", sub.Status)
	}
	due, err := repository.NewSubscriptionRepository(db).ListDueForRenewal(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range due {
		if s.ID == sub.ID {
			t.Fatal("cancelled subscription still due")
		}
	}
	guard := repository.NewSubscriptionRepository(db).(interface {
		GuardSubscription(context.Context, uuid.UUID, func(context.Context) error) error
	})
	called = false
	if err = guard.GuardSubscription(ctx, sub.ID, func(context.Context) error { called = true; return nil }); err != nil || called {
		t.Fatal("cancelled subscription dispatched")
	}
	now := time.Now().Add(time.Minute)
	if err = repo.ProcessRefund(ctx, now, func(_ context.Context, id uuid.UUID) error {
		if id != tx.EnrollmentID {
			t.Error("wrong enrollment")
		}
		return errors.New("academic unavailable")
	}); err != nil {
		t.Fatal(err)
	}
	db.First(&stored, "transaction_id = ?", tx.ID)
	if stored.Status != "pending" || stored.AttemptCount != 1 || stored.LastError == nil {
		t.Fatalf("retry %+v", stored)
	}
	db.First(&tx, "id = ?", tx.ID)
	if tx.Status != "refunded" {
		t.Fatal("refund rolled back")
	}
	if err = repo.ProcessRefund(ctx, now.Add(time.Hour), func(context.Context, uuid.UUID) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = repo.ProcessRefund(ctx, now.Add(2*time.Hour), func(context.Context, uuid.UUID) error { t.Error("completed job repeated"); return nil }); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal(err)
	}
	// An unclaimed activation is withdrawn atomically with the refund.
	pendingTx := domain.Transaction{ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: tenant, ParentID: uuid.New(), StudentID: uuid.New(), EnrollmentID: uuid.New(), Status: "paid", Currency: "IDR"}
	if err := db.Create(&pendingTx).Error; err != nil {
		t.Fatal(err)
	}
	pendingJob := domain.PaymentReconciliation{ID: uuid.New(), TransactionID: pendingTx.ID, EnrollmentID: pendingTx.EnrollmentID, Kind: domain.ReconciliationKindActivation, Status: domain.ReconciliationStatusPending}
	if err := recon.EnsureActivation(ctx, &pendingJob); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordRefund(ctx, tenant, actor, pendingTx.ID, req); err != nil {
		t.Fatal(err)
	}
	if _, err := recon.GetByTransactionID(ctx, pendingTx.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("pending activation survived: %v", err)
	}
	if err := db.Exec("DELETE FROM transaction_refunds WHERE transaction_id = ?", pendingTx.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Delete(&pendingTx).Error; err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{"pending", "failed", "expired", "cancelled", "creating"} {
		other := domain.Transaction{ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: tenant, ParentID: uuid.New(), StudentID: uuid.New(), EnrollmentID: uuid.New(), Status: status, Currency: "IDR"}
		if err = db.Create(&other).Error; err != nil {
			t.Fatal(err)
		}
		_, e := repo.RecordRefund(ctx, tenant, actor, other.ID, req)
		db.Unscoped().Delete(&other)
		if !errors.Is(e, domain.ErrInvalidTransactionStatus) {
			t.Fatalf("status %s: %v", status, e)
		}
	}
}
