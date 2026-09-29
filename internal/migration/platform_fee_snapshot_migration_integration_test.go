package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

const kel99SnapshotVersion = 20260928000000

func kel99Transaction(enrollment uuid.UUID) *domain.Transaction {
	return &domain.Transaction{
		ID: uuid.New(), MerchantOrderID: "kel99-" + uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(),
		StudentID: uuid.New(), EnrollmentID: enrollment, SubtotalAmount: 180000, GrossAmount: 180000,
		PlatformFee: 2000, NetAmount: 178000, Currency: "IDR", Status: domain.TransactionStatusPending,
	}
}

func kel99MustFail(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: want the database to reject it", what)
	}
	if !strings.Contains(err.Error(), "immutable") && !strings.Contains(err.Error(), "chk_transactions_platform_fee_snapshot") {
		t.Fatalf("%s: unexpected error %v", what, err)
	}
}

// TestPlatformFeeSnapshotMigrationPostgres proves the KEL-99 migration on real
// PostgreSQL: legacy rows keep a NULL policy version, a snapshot is written by
// the repository, and neither the snapshot nor the amounts it priced can be
// changed afterwards, while ordinary status updates still work.
func TestPlatformFeeSnapshotMigrationPostgres(t *testing.T) {
	dsn, db := kel59Database(t)
	m := kel59Migrator(t, dsn)
	if err := m.Migrate(kel59IndexVersion); err != nil {
		t.Fatalf("migrate to %d: %v", kel59IndexVersion, err)
	}
	legacy := kel99Transaction(uuid.New())
	repo := repository.NewTransactionRepository(db)
	ctx := context.Background()
	legacyDB := db.Session(&gorm.Session{NewDB: true}).Omit("VANumber", "QRString", "AppURL")
	// A pre-KEL-99 row, inserted before the snapshot columns exist.
	if err := db.Exec(`INSERT INTO transactions (id, merchant_order_id, tenant_id, parent_id, student_id, enrollment_id, subtotal_amount, discount_amount, gross_amount, platform_fee, payment_gateway_fee, net_amount, currency, status, billing_email, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 180000, 0, 180000, 2000, 0, 178000, 'IDR', 'pending', '', now(), now())`,
		legacy.ID, legacy.MerchantOrderID, legacy.TenantID, legacy.ParentID, legacy.StudentID, legacy.EnrollmentID).Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	if err := m.Migrate(kel99SnapshotVersion); err != nil {
		t.Fatalf("migrate to %d: %v", kel99SnapshotVersion, err)
	}

	stored, err := repo.GetByID(ctx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PlatformFeePolicyVersion != nil || stored.PlatformFeePercentBps != nil || stored.PlatformFeeFixed != nil || stored.PlatformFee != 2000 {
		t.Fatalf("legacy row changed by the migration: %+v", stored)
	}
	// A legacy row may still move through its lifecycle...
	if err := db.Exec(`UPDATE transactions SET status = 'failed' WHERE id = ?`, legacy.ID).Error; err != nil {
		t.Fatalf("legacy status update: %v", err)
	}
	// ...but can never be given a policy version afterwards.
	kel99MustFail(t, db.Exec(`UPDATE transactions SET platform_fee_policy_version = 1, platform_fee_percent_bps = 0, platform_fee_fixed = 2000 WHERE id = ?`, legacy.ID).Error, "backfill legacy snapshot")

	// New transaction priced by 5% + Rp1000 (policy version 3).
	breakdown, err := domain.ComputePlatformFee(domain.PlatformFeePolicy{Version: 3, PercentBps: 500, FixedFee: 1000}, 180000, 0)
	if err != nil {
		t.Fatal(err)
	}
	priced := kel99Transaction(uuid.New())
	breakdown.Apply(priced)
	// This historical migration test runs before the later instruction columns exist.
	if err := legacyDB.Create(priced).Error; err != nil {
		t.Fatalf("create priced transaction: %v", err)
	}
	stored = &domain.Transaction{}
	err = legacyDB.First(stored, "id = ?", priced.ID).Error
	if err != nil {
		t.Fatal(err)
	}
	if stored.PlatformFee != 10000 || stored.NetAmount != 170000 || stored.PlatformFeePolicyVersion == nil || *stored.PlatformFeePolicyVersion != 3 || *stored.PlatformFeePercentBps != 500 || *stored.PlatformFeeFixed != 1000 {
		t.Fatalf("stored snapshot = %+v", stored)
	}
	// A status update remains allowed on a row at this historical schema version.
	if err := db.Exec(`UPDATE transactions SET status = 'failed' WHERE id = ?`, stored.ID).Error; err != nil {
		t.Fatalf("status update: %v", err)
	}
	for what, sql := range map[string]string{
		"change policy version": `UPDATE transactions SET platform_fee_policy_version = 4 WHERE id = ?`,
		"change percent":        `UPDATE transactions SET platform_fee_percent_bps = 250 WHERE id = ?`,
		"clear snapshot":        `UPDATE transactions SET platform_fee_policy_version = NULL, platform_fee_percent_bps = NULL, platform_fee_fixed = NULL WHERE id = ?`,
		"reprice fee":           `UPDATE transactions SET platform_fee = 0, net_amount = 180000 WHERE id = ?`,
		"change gross":          `UPDATE transactions SET gross_amount = 200000 WHERE id = ?`,
	} {
		kel99MustFail(t, db.Exec(sql, priced.ID).Error, what)
	}

	// The CHECK rejects a partial or inconsistent snapshot on insert.
	for what, mutate := range map[string]func(*domain.Transaction){
		"partial snapshot":   func(tx *domain.Transaction) { tx.PlatformFeeFixed = nil },
		"fee not from rule":  func(tx *domain.Transaction) { tx.PlatformFee, tx.NetAmount = 9000, 171000 },
		"percent over bound": func(tx *domain.Transaction) { v := int64(2001); tx.PlatformFeePercentBps = &v },
	} {
		tx := kel99Transaction(uuid.New())
		breakdown.Apply(tx)
		mutate(tx)
		kel99MustFail(t, legacyDB.Create(tx).Error, what)
	}

	// Down removes only the snapshot machinery; charged amounts stay.
	if err := m.Steps(-1); err != nil {
		t.Fatalf("down: %v", err)
	}
	var fee int64
	if err := db.Raw(`SELECT platform_fee FROM transactions WHERE id = ?`, priced.ID).Scan(&fee).Error; err != nil || fee != 10000 {
		t.Fatalf("after down platform_fee = %d err = %v, want 10000", fee, err)
	}
	if err := m.Migrate(kel99SnapshotVersion); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("re-up: %v", err)
	}
	var count int64
	if err := db.Raw(`SELECT count(*) FROM transactions WHERE platform_fee_policy_version IS NULL`).Scan(&count).Error; err != nil || count != 2 {
		t.Fatalf("after re-up rows without policy version = %d err = %v, want 2", count, err)
	}
}
