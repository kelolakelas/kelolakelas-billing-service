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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// Uses a disposable database: migrations and row-level claims run against PostgreSQL.
func TestConcurrentPaymentMethodsClaimOnlyOneInvoicePostgres(t *testing.T) {
	dsn := os.Getenv("KEL125_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL125_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	migrationDir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New((&url.URL{Scheme: "file", Path: migrationDir}).String(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
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
	method := "SP"
	tx := &domain.Transaction{
		ID: uuid.New(), MerchantOrderID: uuid.NewString(), EnrollmentID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(),
		Currency: "IDR", Status: domain.TransactionStatusPending, SubtotalAmount: 40000, GrossAmount: 40000, NetAmount: 40000, PaymentMethod: &method,
	}
	if err := db.Create(tx).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Unscoped().Delete(&domain.Transaction{}, "id = ?", tx.ID).Error })
	repo := NewTransactionRepository(db)
	var wg sync.WaitGroup
	claimed := make(chan bool, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := repo.(TransactionLockingRepository).ClaimInvoice(context.Background(), tx.ID, time.Now(), 10)
			claimed <- ok
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(claimed)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	winners := 0
	for ok := range claimed {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("invoice claims=%d, want one", winners)
	}
	issued, err := repo.(TransactionLockingRepository).MarkInvoiceIssued(context.Background(), tx.ID, &domain.CreateInvoiceResponse{PaymentURL: "https://pay.example.test/invoice", Reference: "REF-ONE", VANumber: "7007014001444348", QRString: "QR-PAYLOAD", AppURL: "https://app.example.test/pay"}, time.Now().Add(time.Hour))
	if err != nil || !issued {
		t.Fatalf("issue=%v err=%v", issued, err)
	}
	if ok, err := repo.(TransactionLockingRepository).ClaimInvoice(context.Background(), tx.ID, time.Now(), 10); err != nil || ok {
		t.Fatalf("replay claim=%v err=%v", ok, err)
	}
	stored, err := repo.GetByEnrollmentID(context.Background(), tx.EnrollmentID)
	if err != nil || stored.PaymentMethod == nil || *stored.PaymentMethod != "SP" || stored.CheckoutSessionURL == nil || *stored.CheckoutSessionURL != "https://pay.example.test/invoice" || stored.VANumber == nil || *stored.VANumber != "7007014001444348" || stored.QRString == nil || *stored.QRString != "QR-PAYLOAD" || stored.AppURL == nil || *stored.AppURL != "https://app.example.test/pay" {
		t.Fatalf("stored transaction=%+v err=%v", stored, err)
	}
}
