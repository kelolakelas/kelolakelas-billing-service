package repository

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// KEL142_TEST_DATABASE_URL must point at an isolated, disposable PostgreSQL
// database (for example the kel142-db-test container). The test applies every
// migration so it runs against the real schema, including the partial unique
// index uq_bank_accounts_tenant_primary.
func openKEL142Database(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("KEL142_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL142_TEST_DATABASE_URL for the KEL-142 PostgreSQL integration test")
	}
	migrations, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New((&url.URL{Scheme: "file", Path: migrations}).String(), dsn)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
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

// KEL-142: ledger reads and bank-account writes stay inside one tenant. A
// cross-tenant id answers not-found / returns nothing, never another tenant's
// rows.
func TestKEL142TenantScopeIsolatesLedgerAndBankAccounts(t *testing.T) {
	db := openKEL142Database(t)
	ctx := context.Background()
	tenant, other := uuid.New(), uuid.New()
	tenantWallet := domain.Wallet{ID: uuid.New(), TenantID: tenant}
	if err := db.Create(&tenantWallet).Error; err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	tenantAccount := domain.BankAccount{
		ID: uuid.New(), TenantID: tenant, BankCode: "014",
		AccountNumber: "1111222233", AccountName: "Tenant", IsPrimary: true,
	}
	if err := db.Create(&tenantAccount).Error; err != nil {
		t.Fatalf("seed bank account: %v", err)
	}
	entry := domain.LedgerEntry{
		ID: uuid.New(), WalletID: tenantWallet.ID, ReferenceID: uuid.New(),
		ReferenceType: "transaction", Amount: 90000, EntryType: "payment_received",
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed ledger entry: %v", err)
	}
	t.Cleanup(func() {
		db.Unscoped().Delete(&domain.LedgerEntry{}, "id = ?", entry.ID)
		db.Unscoped().Delete(&domain.BankAccount{}, "id = ?", tenantAccount.ID)
		db.Unscoped().Delete(&domain.Wallet{}, "id = ?", tenantWallet.ID)
	})

	ledger := NewLedgerEntryRepository(db)
	accounts := NewBankAccountRepository(db)

	rows, total, err := ledger.ListByWallet(ctx, tenantWallet.ID, 1, 20)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != entry.ID {
		t.Fatalf("own ledger rows=%v total=%d err=%v, want the seeded entry", rows, total, err)
	}
	foreignRows, foreignTotal, err := ledger.ListByWallet(ctx, uuid.New(), 1, 20)
	if err != nil || foreignTotal != 0 || len(foreignRows) != 0 {
		t.Fatalf("foreign wallet rows=%v total=%d err=%v, want empty", foreignRows, foreignTotal, err)
	}

	if _, err := accounts.GetByIDScoped(ctx, other, tenantAccount.ID); err == nil {
		t.Fatal("cross-tenant GetByIDScoped succeeded, want not found")
	}
	otherAccounts, err := accounts.ListByTenant(ctx, other)
	if err != nil || len(otherAccounts) != 0 {
		t.Fatalf("cross-tenant list=%v err=%v, want empty", otherAccounts, err)
	}
}

// KEL-142: concurrent set-primary promoters serialize on the partial unique
// index so exactly one account stays primary.
func TestKEL142ConcurrentSetPrimaryKeepsExactlyOnePrimary(t *testing.T) {
	db := openKEL142Database(t)
	ctx := context.Background()
	tenant := uuid.New()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	for i, id := range ids {
		account := domain.BankAccount{
			ID: id, TenantID: tenant, BankCode: "014",
			AccountNumber: "9900" + id.String()[:4], AccountName: "Tenant",
			IsPrimary: i == 0,
		}
		if err := db.Create(&account).Error; err != nil {
			t.Fatalf("seed bank account: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, id := range ids {
			db.Unscoped().Delete(&domain.BankAccount{}, "id = ?", id)
		}
	})

	accounts := NewBankAccountRepository(db)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(target uuid.UUID) {
			defer wg.Done()
			_ = db.Transaction(func(tx *gorm.DB) error {
				txCtx := context.WithValue(ctx, txKey{}, tx)
				_, _ = accounts.GetByIDScopedForUpdate(txCtx, tenant, target)
				return accounts.SetPrimary(txCtx, tenant, target)
			})
		}(id)
	}
	wg.Wait()

	live, err := accounts.ListByTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	primaries := 0
	for i := range live {
		if live[i].IsPrimary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("primaries=%d, want exactly one primary after concurrent promotion", primaries)
	}
}
