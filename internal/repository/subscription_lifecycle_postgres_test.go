package repository_test

import (
	"context"
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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

func TestSubscriptionLifecyclePostgresConcurrentSuspendAndLatePayment(t *testing.T) {
	dsn := os.Getenv("KEL150_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL150_TEST_DATABASE_URL to isolated PostgreSQL")
	}
	path, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	migrator, err := migrate.New((&url.URL{Scheme: "file", Path: path}).String(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatal(err)
	}
	migrator.Close()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(10)
	ctx := context.Background()
	period := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	sub := domain.Subscription{ID: uuid.New(), EnrollmentID: uuid.New(), BillingCycle: "monthly", NextBillingDate: period, Status: "active"}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&sub)
	defer db.Exec("DELETE FROM subscription_lifecycle_reconciliations WHERE subscription_id = ?", sub.ID)
	lifecycle := repository.NewSubscriptionRepository(db).(repository.SubscriptionLifecycleRepository)
	now := period.AddDate(0, 0, 8)
	var wg sync.WaitGroup
	errorsCh := make(chan error, 3)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := lifecycle.SuspendOverdue(ctx, sub.ID, period, now); errorsCh <- err }()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var status string
	if err := db.Model(&sub).Select("status").Where("id = ?", sub.ID).Scan(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != "suspended" {
		t.Fatalf("status=%s", status)
	}
	jobs, err := lifecycle.ListLifecycle(ctx, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, j := range jobs {
		if j.SubscriptionID == sub.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("jobs=%d, want 1", count)
	}
	inFlightSuspend, err := lifecycle.ClaimLifecycle(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	// Race a real paid row (Duitku confirmation precedes this transaction) with
	// another overdue sweep. The shared subscription lock must leave it active.
	tx := domain.Transaction{ID: uuid.New(), MerchantOrderID: "kel150-" + uuid.NewString(), EnrollmentID: sub.EnrollmentID, TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), SubscriptionID: &sub.ID, BillingPeriodStart: &period, Status: domain.TransactionStatusPending, Currency: "IDR", GrossAmount: 1000}
	if err := db.Create(&tx).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Unscoped().Delete(&tx)
	// Expiry of a renewal is bookkeeping, not a permanent seat release.
	expiry := now.Add(-time.Hour)
	if err := db.Model(&tx).Updates(map[string]interface{}{"invoice_expires_at": expiry}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repository.NewTransactionRepository(db).(repository.TransactionExpiryRepository).ExpireDue(ctx, now, 10); err != nil {
		t.Fatal(err)
	}
	var releases int64
	if err := db.Table("payment_reconciliations").Where("transaction_id = ?", tx.ID).Count(&releases).Error; err != nil {
		t.Fatal(err)
	}
	if releases != 0 {
		t.Fatalf("expired renewal queued %d permanent seat releases", releases)
	}
	if err := repository.NewTransactionManager(db).WithTransaction(ctx, func(txCtx context.Context) error {
		if err := repository.GetDB(txCtx, db).Model(&tx).Update("status", domain.TransactionStatusPaid).Error; err != nil {
			return err
		}
		if err := lifecycle.ResumePaid(txCtx, sub.ID, sub.EnrollmentID, now); err != nil {
			return err
		}
		return repository.GetDB(txCtx, db).Model(&sub).Updates(map[string]interface{}{"status": "active", "next_billing_date": period.AddDate(0, 1, 0)}).Error
	}); err != nil {
		t.Fatal(err)
	}
	var race sync.WaitGroup
	raceErrors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		race.Add(1)
		go func() {
			defer race.Done()
			_, err := lifecycle.SuspendOverdue(ctx, sub.ID, period, now)
			raceErrors <- err
		}()
	}
	race.Wait()
	close(raceErrors)
	for err := range raceErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&sub).Select("status").Where("id = ?", sub.ID).Scan(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("paid subscription status=%s", status)
	}
	jobs, err = lifecycle.ListLifecycle(ctx, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.SubscriptionID == sub.ID && (j.DesiredAction != "resume" || j.Status != "processing") {
			t.Fatalf("job after paid callback: %+v", j)
		}
	}
	// A suspend claimed before the payment cannot erase its newer resume request.
	if err := lifecycle.FinishLifecycle(ctx, inFlightSuspend, now, "", 10); err != nil {
		t.Fatal(err)
	}
	claimed, err := lifecycle.ClaimLifecycle(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.SubscriptionID != sub.ID || claimed.DesiredAction != "resume" {
		t.Fatalf("unexpected resume claim: %+v", claimed)
	}
	if err := lifecycle.FinishLifecycle(ctx, claimed, now, "", 10); err != nil {
		t.Fatal(err)
	}
	jobs, err = lifecycle.ListLifecycle(ctx, "active", 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range jobs {
		if j.SubscriptionID == sub.ID && j.DesiredAction == "resume" {
			found = true
		}
	}
	if !found {
		t.Fatal("late payment resume was not completed")
	}
}
