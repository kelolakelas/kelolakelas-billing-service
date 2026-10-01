package repository_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

// KEL161_TEST_DATABASE_URL must point at an isolated, disposable PostgreSQL
// database (for example the kel161-db-test container). The test applies every
// migration so it runs against the real schema, including the exact-match
// unique index uq_vouchers_tenant_code. Legacy mixed-case rows require the
// tenant-scoped check under the transaction advisory lock.
func openKEL161Database(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("KEL161_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL161_TEST_DATABASE_URL for the KEL-161 PostgreSQL integration test")
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
	sqlDB.SetMaxOpenConns(10)
	t.Cleanup(func() { sqlDB.Close() })
	return db
}

func seedKEL161Voucher(t *testing.T, db *gorm.DB, tenant uuid.UUID, code string, currentUses int) domain.Voucher {
	t.Helper()
	v := domain.Voucher{
		ID: uuid.New(), TenantID: tenant, Code: code,
		DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10,
		CurrentUses: currentUses, IsActive: true,
	}
	if err := db.Create(&v).Error; err != nil {
		t.Fatalf("seed voucher: %v", err)
	}
	t.Cleanup(func() { db.Unscoped().Delete(&domain.Voucher{}, "id = ?", v.ID) })
	return v
}

// KEL-161: voucher reads stay inside one tenant. A cross-tenant id answers
// not-found and the list returns nothing, never another tenant's rows.
func TestKEL161VoucherTenantScopeIsolatesReads(t *testing.T) {
	db := openKEL161Database(t)
	ctx := context.Background()
	tenant, other := uuid.New(), uuid.New()
	seeded := seedKEL161Voucher(t, db, tenant, "HEMAT10", 2)

	vouchers := repository.NewVoucherRepository(db)

	got, err := vouchers.GetByIDScoped(ctx, tenant, seeded.ID)
	if err != nil || got.CurrentUses != 2 {
		t.Fatalf("own GetByIDScoped=%+v err=%v, want usage count 2", got, err)
	}
	if _, err := vouchers.GetByIDScoped(ctx, other, seeded.ID); err == nil {
		t.Fatal("cross-tenant GetByIDScoped succeeded, want not found")
	}
	foreign, total, err := vouchers.ListByTenant(ctx, other, 1, 20)
	if err != nil || total != 0 || len(foreign) != 0 {
		t.Fatalf("cross-tenant list=%v total=%d err=%v, want empty", foreign, total, err)
	}
	own, total, err := vouchers.ListByTenant(ctx, tenant, 1, 20)
	if err != nil || total != 1 || len(own) != 1 || own[0].ID != seeded.ID {
		t.Fatalf("own list=%v total=%d err=%v, want the seeded voucher", own, total, err)
	}
	// GetByCode matches case-insensitively inside the tenant but never across.
	if _, err := vouchers.GetByCode(ctx, tenant, "hemat10"); err != nil {
		t.Fatalf("own GetByCode lowercase: %v", err)
	}
	if _, err := vouchers.GetByCode(ctx, other, "HEMAT10"); err == nil {
		t.Fatal("cross-tenant GetByCode succeeded, want not found")
	}
}

// KEL-161: the same code in different case collides inside one tenant (the
// usecase maps the backstop to a duplicate), while another tenant may reuse
// the code freely.
func TestKEL161VoucherCodeUniqueCaseInsensitivePerTenant(t *testing.T) {
	db := openKEL161Database(t)
	ctx := context.Background()
	tenant, other := uuid.New(), uuid.New()
	vouchers := repository.NewVoucherRepository(db)
	service := usecase.NewVoucherUsecase(vouchers, repository.NewTransactionManager(db))

	if _, err := service.Create(ctx, tenant, &domain.CreateVoucherRequest{
		Code: "Hemat10", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10,
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	t.Cleanup(func() { db.Unscoped().Where("tenant_id = ?", tenant).Delete(&domain.Voucher{}) })
	t.Cleanup(func() { db.Unscoped().Where("tenant_id = ?", other).Delete(&domain.Voucher{}) })

	if _, err := service.Create(ctx, tenant, &domain.CreateVoucherRequest{
		Code: "hemat10", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10,
	}); err == nil {
		t.Fatal("same-tenant different-case create succeeded, want duplicate")
	} else if err != domain.ErrVoucherDuplicate {
		t.Fatalf("err=%v, want ErrVoucherDuplicate", err)
	}

	if _, err := service.Create(ctx, other, &domain.CreateVoucherRequest{
		Code: "hemat10", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10,
	}); err != nil {
		t.Fatalf("other-tenant create: %v", err)
	}
}

// Existing mixed-case rows must also reserve the code for the tenant. The
// original exact-match constraint alone does not prevent this collision.
func TestKEL161LegacyMixedCaseVoucherRejectsCreateAndRename(t *testing.T) {
	db := openKEL161Database(t)
	ctx := context.Background()
	tenant, other := uuid.New(), uuid.New()
	legacy := seedKEL161Voucher(t, db, tenant, "Legacy10", 0)
	vouchers := repository.NewVoucherRepository(db)
	service := usecase.NewVoucherUsecase(vouchers, repository.NewTransactionManager(db))
	t.Cleanup(func() { db.Unscoped().Where("tenant_id = ?", tenant).Delete(&domain.Voucher{}) })
	t.Cleanup(func() { db.Unscoped().Where("tenant_id = ?", other).Delete(&domain.Voucher{}) })

	create := &domain.CreateVoucherRequest{Code: "legacy10", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10}
	if _, err := service.Create(ctx, tenant, create); !errors.Is(err, domain.ErrVoucherDuplicate) {
		t.Fatalf("same-tenant legacy collision err=%v, want ErrVoucherDuplicate", err)
	}
	rows, total, err := vouchers.ListByTenant(ctx, tenant, 1, 20)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != legacy.ID {
		t.Fatalf("rows=%v total=%d err=%v, want only legacy row", rows, total, err)
	}
	candidate := seedKEL161Voucher(t, db, tenant, "OTHER10", 0)
	rename := "LEGACY10"
	if _, err := service.Update(ctx, tenant, candidate.ID, &domain.UpdateVoucherRequest{Code: &rename}); !errors.Is(err, domain.ErrVoucherDuplicate) {
		t.Fatalf("rename into legacy code err=%v, want ErrVoucherDuplicate", err)
	}
	if _, err := service.Create(ctx, other, create); err != nil {
		t.Fatalf("other tenant may reuse legacy code: %v", err)
	}
}

// An unchanged legacy code can only be canonicalized if no other live row
// already has its case-insensitive equivalent.
func TestKEL161LegacyDuplicateBlocksCanonicalizingUpdate(t *testing.T) {
	db := openKEL161Database(t)
	tenant := uuid.New()
	first := seedKEL161Voucher(t, db, tenant, "Legacy10", 0)
	seedKEL161Voucher(t, db, tenant, "LEGACY10", 0)
	service := usecase.NewVoucherUsecase(repository.NewVoucherRepository(db), repository.NewTransactionManager(db))
	code := "legacy10"
	if _, err := service.Update(context.Background(), tenant, first.ID, &domain.UpdateVoucherRequest{Code: &code}); !errors.Is(err, domain.ErrVoucherDuplicate) {
		t.Fatalf("legacy duplicate update err=%v, want ErrVoucherDuplicate", err)
	}
}

// KEL-161: concurrent creators racing the same code (in different cases) leave
// exactly one live voucher: the canonical form makes the exact-match unique
// index decide the winner.
func TestKEL161ConcurrentVoucherCreateKeepsOneRow(t *testing.T) {
	db := openKEL161Database(t)
	ctx := context.Background()
	tenant := uuid.New()
	vouchers := repository.NewVoucherRepository(db)
	service := usecase.NewVoucherUsecase(vouchers, repository.NewTransactionManager(db))
	t.Cleanup(func() { db.Unscoped().Where("tenant_id = ?", tenant).Delete(&domain.Voucher{}) })

	codes := []string{"race10", "RACE10", "Race10", "rAcE10"}
	var created, duplicates int64
	var wg sync.WaitGroup
	for _, code := range codes {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			_, err := service.Create(ctx, tenant, &domain.CreateVoucherRequest{
				Code: c, DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10,
			})
			switch err {
			case nil:
				atomic.AddInt64(&created, 1)
			case domain.ErrVoucherDuplicate:
				atomic.AddInt64(&duplicates, 1)
			default:
				t.Errorf("create %q err=%v, want nil or duplicate", c, err)
			}
		}(code)
	}
	wg.Wait()
	if created != 1 || duplicates != int64(len(codes)-1) {
		t.Fatalf("created=%d duplicates=%d, want 1 winner and %d losers", created, duplicates, len(codes)-1)
	}
	rows, total, err := vouchers.ListByTenant(ctx, tenant, 1, 20)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Code != "RACE10" {
		t.Fatalf("rows=%v total=%d err=%v, want one canonical RACE10", rows, total, err)
	}
}
