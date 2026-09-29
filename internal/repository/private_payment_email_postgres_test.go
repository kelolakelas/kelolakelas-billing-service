package repository

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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPrivatePaymentEmailClaimsPostgres(t *testing.T) {
	dsn := os.Getenv("KEL128_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL128_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	dir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New((&url.URL{Scheme: "file", Path: dir}).String(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatal(err)
	}
	_, _ = m.Close()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	now := time.Now().UTC().Round(time.Microsecond)
	link, reference := "https://pay.example.test/invoice", "REF-ONE"
	expires := now.Add(time.Hour)
	tx := &domain.Transaction{ID: uuid.New(), MerchantOrderID: uuid.NewString(), EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), Currency: "IDR", Status: domain.TransactionStatusPending, SubtotalAmount: 40000, GrossAmount: 40000, NetAmount: 40000, BillingEmail: "parent@example.test", PrivateScheduleRequest: true, CheckoutSessionURL: &link, PaymentIntentID: &reference, InvoiceExpiresAt: &expires}
	if err = db.Create(tx).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Unscoped().Delete(&domain.Transaction{}, "id = ?", tx.ID).Error })
	repo := NewTransactionRepository(db).(PrivatePaymentEmailRepository)
	rows, err := repo.ListPrivatePaymentEmails(context.Background(), now, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ID == tx.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("private invoice not listed")
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := repo.ClaimPrivatePaymentEmail(context.Background(), tx.ID, now, 10*time.Minute)
			if err != nil {
				t.Error(err)
			}
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("claims=%d", winners)
	}
	ok, err := repo.FinishPrivatePaymentEmail(context.Background(), tx.ID, now, "resend unavailable")
	if err != nil || !ok {
		t.Fatalf("release=%v err=%v", ok, err)
	}
	stored, err := NewTransactionRepository(db).GetByID(context.Background(), tx.ID)
	if err != nil || stored.PrivatePaymentEmailClaimedAt != nil || stored.PrivatePaymentEmailFailureReason == nil {
		t.Fatalf("failure not observable: %+v %v", stored, err)
	}
	later := now.Add(time.Second)
	ok, err = repo.ClaimPrivatePaymentEmail(context.Background(), tx.ID, later, 10*time.Minute)
	if err != nil || !ok {
		t.Fatalf("retry=%v err=%v", ok, err)
	}
	ok, err = repo.FinishPrivatePaymentEmail(context.Background(), tx.ID, later, "")
	if err != nil || !ok {
		t.Fatalf("finish=%v err=%v", ok, err)
	}
	ok, err = repo.ClaimPrivatePaymentEmail(context.Background(), tx.ID, later.Add(time.Second), 10*time.Minute)
	if err != nil || ok {
		t.Fatalf("duplicate=%v err=%v", ok, err)
	}
	stored, err = NewTransactionRepository(db).GetByID(context.Background(), tx.ID)
	if err != nil || stored.PrivatePaymentEmailSentAt == nil || stored.PrivatePaymentEmailFailureReason != nil {
		t.Fatalf("success not persisted: %+v %v", stored, err)
	}
	// A fresh invoice replaces the old URL and needs its own delivery; a paid
	// callback after issuance prevents a new claim.
	if err := db.Model(&domain.Transaction{}).Where("id = ?", tx.ID).Updates(map[string]interface{}{"status": domain.TransactionStatusCreating}).Error; err != nil {
		t.Fatal(err)
	}
	issued, err := NewTransactionRepository(db).(TransactionLockingRepository).MarkInvoiceIssued(context.Background(), tx.ID, &domain.CreateInvoiceResponse{PaymentURL: "https://pay.example.test/reissued", Reference: "REF-TWO"}, later.Add(time.Hour))
	if err != nil || !issued {
		t.Fatalf("reissue=%v err=%v", issued, err)
	}
	ok, err = repo.ClaimPrivatePaymentEmail(context.Background(), tx.ID, later.Add(2*time.Second), 10*time.Minute)
	if err != nil || !ok {
		t.Fatalf("new invoice claim=%v err=%v", ok, err)
	}
	if err := db.Model(&domain.Transaction{}).Where("id = ?", tx.ID).Update("status", domain.TransactionStatusPaid).Error; err != nil {
		t.Fatal(err)
	}
	ok, err = repo.FinishPrivatePaymentEmail(context.Background(), tx.ID, later.Add(2*time.Second), "provider failed")
	if err != nil || ok {
		t.Fatalf("paid callback overwritten=%v err=%v", ok, err)
	}
}
