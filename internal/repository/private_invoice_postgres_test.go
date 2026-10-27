package repository

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestKEL226PrivateInvoicePostgresReadOnlyScope(t *testing.T) {
	dsn := os.Getenv("KEL226_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL226_TEST_DATABASE_URL to a disposable database")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := "kel226_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	cfg.RuntimeParams["search_path"] = schema + ",public"
	cfg.RuntimeParams["timezone"] = "UTC"
	pool := stdlib.OpenDB(*cfg)
	defer pool.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("../../migrations/*.up.sql")
	sort.Strings(files)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Exec(strings.ReplaceAll(string(raw), "CREATE EXTENSION IF NOT EXISTS pgcrypto;", "")).Error; err != nil {
			t.Fatalf("%s %v", file, err)
		}
	}
	scope := domain.PrivateInvoiceScope{TransactionID: uuid.New(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New()}
	scope.EnrollmentID = scope.TransactionID
	checkout := "https://pay.example.test/private"
	expiry := time.Now().UTC().Add(time.Hour)
	tx := domain.Transaction{ID: scope.TransactionID, EnrollmentID: scope.EnrollmentID, TenantID: scope.TenantID, ParentID: scope.ParentID, StudentID: scope.StudentID, MerchantOrderID: uuid.NewString(), Status: "pending", PrivateScheduleRequest: true, CheckoutSessionURL: &checkout, InvoiceExpiresAt: &expiry, ClassName: "Private", GrossAmount: 100000, SubtotalAmount: 100000, NetAmount: 100000}
	if err = db.Create(&tx).Error; err != nil {
		t.Fatal(err)
	}
	reader := NewPrivateInvoiceReader(db)
	ctx := context.Background()
	for _, name := range []string{"valid", "transaction", "enrollment", "tenant", "parent", "student"} {
		t.Run(name, func(t *testing.T) {
			bad := scope
			switch name {
			case "transaction":
				bad.TransactionID = uuid.New()
			case "enrollment":
				bad.EnrollmentID = uuid.New()
			case "tenant":
				bad.TenantID = uuid.New()
			case "parent":
				bad.ParentID = uuid.New()
			case "student":
				bad.StudentID = uuid.New()
			}
			row, err := reader.ReadPrivateInvoice(ctx, bad)
			if name == "valid" {
				if err != nil || row.ID != tx.ID || row.CheckoutSessionURL == nil || *row.CheckoutSessionURL != checkout || row.InvoiceExpiresAt == nil {
					t.Fatalf("read %+v %v", row, err)
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("scope leak %v", err)
			}
		})
	}
	if err = db.Model(&tx).Update("status", "paid").Error; err != nil {
		t.Fatal(err)
	}
	row, err := reader.ReadPrivateInvoice(ctx, scope)
	if err != nil || row.Status != "paid" {
		t.Fatal("stale payment read")
	}
	var after domain.Transaction
	db.First(&after, "id=?", tx.ID)
	if after.GrossAmount != tx.GrossAmount || after.CheckoutSessionURL == nil || *after.CheckoutSessionURL != checkout {
		t.Fatal("read mutated invoice")
	}
	if err = db.Model(&tx).Update("private_schedule_request", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ReadPrivateInvoice(ctx, scope); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("nonprivate invoice leaked")
	}
}
