package repository

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
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

// KEL58_TEST_DATABASE_URL must point at an isolated, disposable PostgreSQL database; the
// test applies every migration so it runs against the real schema, not a GORM model.
func openSalesSummaryDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("KEL58_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL58_TEST_DATABASE_URL for the PostgreSQL sales summary integration test")
	}
	migrations, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New((&url.URL{Scheme: "file", Path: migrations}).String(), dsn)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate up: %v", err)
	}
	m.Close()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return db
}

type seededTransaction struct {
	tenant   uuid.UUID
	status   string
	currency string
	gross    int64
	net      int64
	paidAt   *time.Time
	deleted  bool
}

func seedSalesTransactions(t *testing.T, db *gorm.DB, rows []seededTransaction) {
	t.Helper()
	for _, row := range rows {
		tx := domain.Transaction{
			ID: uuid.New(), MerchantOrderID: "kel58-" + uuid.NewString(), TenantID: row.tenant,
			ParentID: uuid.New(), StudentID: uuid.New(), EnrollmentID: uuid.New(),
			SubtotalAmount: row.gross, GrossAmount: row.gross, PlatformFee: row.gross - row.net, NetAmount: row.net,
			Currency: row.currency, Status: row.status, BillingEmail: "parent@example.com", PaidAt: row.paidAt,
		}
		if err := db.Create(&tx).Error; err != nil {
			t.Fatalf("seed transaction: %v", err)
		}
		if row.deleted {
			if err := db.Delete(&tx).Error; err != nil {
				t.Fatalf("soft delete: %v", err)
			}
		}
		t.Cleanup(func() { db.Unscoped().Delete(&domain.Transaction{}, "id = ?", tx.ID) })
	}
}

func at(value time.Time) *time.Time { return &value }

// KEL-58 AC1/AC2 against PostgreSQL: the totals equal the sum of this tenant's paid
// transactions whose paid_at is inside [from, until), per currency. Other tenants,
// non-paid statuses, soft-deleted rows, and rows on either side of the boundaries do not count.
func TestSummarizePaidPostgresIsolatesTenantStatusAndRange(t *testing.T) {
	db := openSalesSummaryDatabase(t)
	repo := NewTransactionRepository(db).(*transactionRepository)
	tenant, otherTenant := uuid.New(), uuid.New()
	from := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)

	seedSalesTransactions(t, db, []seededTransaction{
		// Counted: the first instant, the middle, and the last microsecond before until.
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 100000, net: 90000, paidAt: at(from)},
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 250000, net: 225000, paidAt: at(from.AddDate(0, 0, 14))},
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 50000, net: 45000, paidAt: at(until.Add(-time.Microsecond))},
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "USD", gross: 20, net: 18, paidAt: at(from.AddDate(0, 0, 3))},
		// Not counted: outside the range.
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 7000, net: 6000, paidAt: at(from.Add(-time.Microsecond))},
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 8000, net: 7000, paidAt: at(until)},
		// Not counted: other tenant, non-paid statuses (even with a paid_at), soft-deleted.
		{tenant: otherTenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 900000, net: 800000, paidAt: at(from.AddDate(0, 0, 2))},
		{tenant: tenant, status: domain.TransactionStatusRefunded, currency: "IDR", gross: 11000, net: 10000, paidAt: at(from.AddDate(0, 0, 5))},
		{tenant: tenant, status: domain.TransactionStatusPending, currency: "IDR", gross: 12000, net: 11000},
		{tenant: tenant, status: domain.TransactionStatusExpired, currency: "IDR", gross: 13000, net: 12000},
		{tenant: tenant, status: domain.TransactionStatusCancelled, currency: "IDR", gross: 14000, net: 13000},
		{tenant: tenant, status: domain.TransactionStatusFailed, currency: "IDR", gross: 15000, net: 14000},
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 16000, net: 15000, paidAt: at(from.AddDate(0, 0, 6)), deleted: true},
	})

	totals, err := repo.SummarizePaid(context.Background(), tenant, from, until)
	if err != nil {
		t.Fatalf("SummarizePaid: %v", err)
	}
	want := []domain.SalesSummary{
		{Currency: "IDR", TransactionCount: 3, GrossAmount: 400000, NetAmount: 360000},
		{Currency: "USD", TransactionCount: 1, GrossAmount: 20, NetAmount: 18},
	}
	if len(totals) != len(want) || totals[0] != want[0] || totals[1] != want[1] {
		t.Fatalf("totals=%+v, want %+v", totals, want)
	}

	other, err := repo.SummarizePaid(context.Background(), otherTenant, from, until)
	if err != nil || len(other) != 1 || other[0].GrossAmount != 900000 {
		t.Fatalf("other tenant totals=%+v err=%v, want only its own row", other, err)
	}

	empty, err := repo.SummarizePaid(context.Background(), uuid.New(), from, until)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("tenant without sales totals=%#v err=%v, want an empty non-nil slice", empty, err)
	}
}
