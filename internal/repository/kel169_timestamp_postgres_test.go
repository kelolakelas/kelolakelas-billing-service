package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/database"
)

func TestKEL169InvoicePostgresUTC(t *testing.T) {
	dsn := os.Getenv("KEL169_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL169_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	path, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New((&url.URL{Scheme: "file", Path: path}).String(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatal(err)
	}
	_, _ = m.Close()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Use the production constructor to cover its UTC DSN and GORM clock.
	db, err := database.NewPostgresDB(parsed.Hostname(), parsed.Port(), parsed.User.Username(), "test-only", strings.TrimPrefix(parsed.Path, "/"), "disable", "disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	old := time.Local
	t.Cleanup(func() { time.Local = old })
	for _, zone := range []string{"Asia/Jakarta", "UTC"} {
		t.Run(zone, func(t *testing.T) {
			time.Local, err = time.LoadLocation(zone)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().Round(time.Microsecond)
			tx := &domain.Transaction{ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), EnrollmentID: uuid.Nil, Currency: "IDR", Status: domain.TransactionStatusPending, SubtotalAmount: 40000, GrossAmount: 40000, NetAmount: 40000}
			repo := NewTransactionRepository(db)
			if err := repo.Create(context.Background(), tx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Unscoped().Delete(&domain.Transaction{}, "id = ?", tx.ID).Error })
			if tx.CreatedAt.Location() != time.UTC {
				t.Fatalf("GORM created_at not UTC: %v", tx.CreatedAt)
			}
			expiry := now.Add(time.Hour) // deliberately supply a local clock at the repository boundary
			locking := repo.(TransactionLockingRepository)
			issued, err := locking.MarkInvoiceIssued(context.Background(), tx.ID, &domain.CreateInvoiceResponse{PaymentURL: "https://pay.example.test", Reference: uuid.NewString()}, expiry)
			if err != nil || !issued {
				t.Fatalf("issue: %v %v", issued, err)
			}
			stored, err := repo.GetByID(context.Background(), tx.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.InvoiceExpiresAt == nil || !stored.InvoiceExpiresAt.Equal(expiry) || stored.InvoiceExpiresAt.Location() != time.UTC {
				t.Fatalf("stored expiry shifted: %v want %v", stored.InvoiceExpiresAt, expiry)
			}
			data, err := json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "+07:00") {
				t.Fatalf("local JSON: %s", data)
			}
			expiryRepo := repo.(TransactionExpiryRepository)
			if n, err := expiryRepo.ExpireDue(context.Background(), now, 200); err != nil || n != 0 {
				t.Fatalf("future invoice expired: %d %v", n, err)
			}
			if ok, err := locking.ClaimReinvoice(context.Background(), tx.ID, now, 10); err != nil || ok {
				t.Fatalf("future invoice reclaimed: %v %v", ok, err)
			}
			// At the exact boundary expiry is inclusive; retries have no duplicate effect.
			if n, err := expiryRepo.ExpireDue(context.Background(), expiry, 200); err != nil || n != 1 {
				t.Fatalf("due invoice: %d %v", n, err)
			}
			if n, err := expiryRepo.ExpireDue(context.Background(), expiry, 200); err != nil || n != 0 {
				t.Fatalf("retry: %d %v", n, err)
			}
			stored, err = repo.GetByID(context.Background(), tx.ID)
			if err != nil || stored.Status != domain.TransactionStatusExpired || stored.ExpiredAt == nil || !stored.ExpiredAt.Equal(expiry) {
				t.Fatalf("expired state: %+v %v", stored, err)
			}
		})
	}
}
