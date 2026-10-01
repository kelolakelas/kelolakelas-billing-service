package repository_test

import (
	"context"
	"errors"
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
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

// KEL143_TEST_DATABASE_URL must point at an isolated, disposable PostgreSQL
// database (for example the kel143-db-test container). The test applies every
// migration so it runs against the real schema, including the withdrawal
// destination snapshot and idempotency backstop.
func openKEL143Database(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("KEL143_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KEL143_TEST_DATABASE_URL for the KEL-143 PostgreSQL integration test")
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

type withdrawalFixture struct {
	db      *gorm.DB
	tenant  uuid.UUID
	wallet  domain.Wallet
	account domain.BankAccount
	service usecase.WithdrawalUsecase
}

func seedWithdrawalFixture(t *testing.T, db *gorm.DB, available int64) *withdrawalFixture {
	t.Helper()
	f := &withdrawalFixture{db: db, tenant: uuid.New()}
	f.wallet = domain.Wallet{ID: uuid.New(), TenantID: f.tenant, AvailableBalance: available}
	f.account = domain.BankAccount{
		ID: uuid.New(), TenantID: f.tenant, BankCode: "014", AccountNumber: "111122223333",
		AccountName: "Tenant", IsPrimary: true,
	}
	for _, row := range []any{&f.wallet, &f.account} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}
	if available > 0 {
		credit := domain.LedgerEntry{
			ID: uuid.New(), WalletID: f.wallet.ID, ReferenceID: uuid.New(),
			ReferenceType: "transaction", Amount: available, EntryType: domain.LedgerEntryTypePayment,
		}
		if err := db.Create(&credit).Error; err != nil {
			t.Fatalf("seed credit: %v", err)
		}
	}
	f.service = usecase.NewWithdrawalUsecase(
		repository.NewWalletRepository(db), repository.NewLedgerEntryRepository(db),
		repository.NewBankAccountRepository(db), repository.NewWithdrawalRepository(db),
		repository.NewTransactionManager(db), 50000,
	)
	t.Cleanup(func() {
		db.Exec("DELETE FROM ledger_entries WHERE wallet_id = ?", f.wallet.ID)
		db.Exec("DELETE FROM withdrawals WHERE tenant_id = ?", f.tenant)
		db.Unscoped().Delete(&domain.BankAccount{}, "tenant_id = ?", f.tenant)
		db.Delete(&domain.Wallet{}, "tenant_id = ?", f.tenant)
	})
	return f
}

func withdrawalBalances(t *testing.T, f *withdrawalFixture) (domain.Wallet, int64, []domain.LedgerEntry) {
	t.Helper()
	var wallet domain.Wallet
	if err := f.db.First(&wallet, "tenant_id = ?", f.tenant).Error; err != nil {
		t.Fatal(err)
	}
	var sum int64
	if err := f.db.Model(&domain.LedgerEntry{}).Where("wallet_id = ?", wallet.ID).Select("COALESCE(SUM(amount), 0)").Scan(&sum).Error; err != nil {
		t.Fatal(err)
	}
	var rows []domain.LedgerEntry
	if err := f.db.Where("wallet_id = ?", wallet.ID).Order("created_at ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return wallet, sum, rows
}

// Two concurrent requests that exceed the available balance in total must
// never double-spend. The wallet row lock serializes the balance check,
// single-open guard, balance move, withdrawal and hold ledger write.
func TestKEL143ConcurrentRequestsHoldOnlyOnce(t *testing.T) {
	db := openKEL143Database(t)
	f := seedWithdrawalFixture(t, db, 100000)
	ctx := context.Background()
	start := make(chan struct{})
	out := make(chan error, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"request-a", "request-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			<-start
			_, err := f.service.RequestWithdrawal(ctx, f.tenant, domain.RequestWithdrawalInput{Amount: 75000, IdempotencyKey: key})
			out <- err
		}(key)
	}
	close(start)
	wg.Wait()
	close(out)
	ok, rejected := 0, 0
	for err := range out {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrWithdrawalOpenExists), errors.Is(err, domain.ErrWithdrawalInsufficientBalance):
			rejected++
		default:
			t.Fatalf("unexpected request error: %v", err)
		}
	}
	if ok != 1 || rejected != 1 {
		t.Fatalf("requests: successful=%d rejected=%d, want 1/1", ok, rejected)
	}
	wallet, sum, entries := withdrawalBalances(t, f)
	if wallet.AvailableBalance != 25000 || wallet.PendingBalance != 75000 || !domain.WalletLedgerInvariant(&wallet, sum, 75000) {
		t.Fatalf("wallet=%+v ledger sum=%d, want 25000 available + 75000 held", wallet, sum)
	}
	if len(entries) != 2 || entries[1].EntryType != domain.LedgerEntryTypeHold || entries[1].Amount != -75000 {
		t.Fatalf("ledger=%+v, want payment + one hold", entries)
	}
	var count int64
	if err := db.Model(&domain.Withdrawal{}).Where("tenant_id = ?", f.tenant).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("withdrawals=%d err=%v, want 1", count, err)
	}
}

// Retry after a committed response returns the same row and does not move
// balance again. A mismatched amount for the same key must not rewrite it.
func TestKEL143IdempotentRetryAndMismatch(t *testing.T) {
	db := openKEL143Database(t)
	f := seedWithdrawalFixture(t, db, 120000)
	ctx := context.Background()
	input := domain.RequestWithdrawalInput{Amount: 60000, IdempotencyKey: "request-retry"}
	first, err := f.service.RequestWithdrawal(ctx, f.tenant, input)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.service.RequestWithdrawal(ctx, f.tenant, input)
	if err != nil || retry.ID != first.ID {
		t.Fatalf("retry=%+v err=%v, want same withdrawal %s", retry, err, first.ID)
	}
	_, err = f.service.RequestWithdrawal(ctx, f.tenant, domain.RequestWithdrawalInput{Amount: 75000, IdempotencyKey: input.IdempotencyKey})
	if !errors.Is(err, domain.ErrWithdrawalIdempotencyMismatch) {
		t.Fatalf("mismatched key err=%v, want conflict", err)
	}
	wallet, sum, entries := withdrawalBalances(t, f)
	if wallet.AvailableBalance != 60000 || wallet.PendingBalance != 60000 || !domain.WalletLedgerInvariant(&wallet, sum, 60000) || len(entries) != 2 {
		t.Fatalf("retry changed money: wallet=%+v sum=%d entries=%d", wallet, sum, len(entries))
	}
}

// A primary change and subsequent edit of the original account must not
// rewrite the payout destination snapshot on an already stored request.
func TestKEL143WithdrawalDestinationIsSnapshot(t *testing.T) {
	db := openKEL143Database(t)
	f := seedWithdrawalFixture(t, db, 120000)
	ctx := context.Background()
	created, err := f.service.RequestWithdrawal(ctx, f.tenant, domain.RequestWithdrawalInput{Amount: 60000, IdempotencyKey: "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	second := domain.BankAccount{ID: uuid.New(), TenantID: f.tenant, BankCode: "008", AccountNumber: "999900001111", AccountName: "Second"}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Unscoped().Delete(&domain.BankAccount{}, "id = ?", second.ID) })
	if err := repository.NewTransactionManager(db).WithTransaction(ctx, func(ctx context.Context) error {
		return repository.NewBankAccountRepository(db).SetPrimary(ctx, f.tenant, second.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.BankAccount{}).Where("id = ?", f.account.ID).Updates(map[string]any{"account_number": "444455556666", "account_name": "Edited"}).Error; err != nil {
		t.Fatal(err)
	}
	found, err := f.service.GetWithdrawal(ctx, f.tenant, created.ID)
	if err != nil || found.BankCode != "014" || found.AccountName != "Tenant" || found.AccountNumber != "********3333" {
		t.Fatalf("snapshot=%+v err=%v, want original masked destination", found, err)
	}
	if _, err := f.service.GetWithdrawal(ctx, uuid.New(), created.ID); !errors.Is(err, domain.ErrWithdrawalNotFound) {
		t.Fatalf("foreign fetch err=%v, want 404 sentinel", err)
	}
	page, err := f.service.ListWithdrawals(ctx, f.tenant, domain.WithdrawalQuery{Page: 1, PageSize: 20})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != created.ID || page.Pagination.TotalItems != 1 {
		t.Fatalf("history=%+v err=%v, want one row", page, err)
	}
}

// A tenant cancel releases exactly once with a reversal ledger entry. A
// second cancel is 409. A foreign tenant cannot cancel or read the request.
func TestKEL143CancelRestoresBalanceAndLedger(t *testing.T) {
	db := openKEL143Database(t)
	f := seedWithdrawalFixture(t, db, 120000)
	ctx := context.Background()
	created, err := f.service.RequestWithdrawal(ctx, f.tenant, domain.RequestWithdrawalInput{Amount: 60000, IdempotencyKey: "cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CancelWithdrawal(ctx, uuid.New(), created.ID); !errors.Is(err, domain.ErrWithdrawalNotFound) {
		t.Fatalf("foreign cancel err=%v, want 404 sentinel", err)
	}
	cancelled, err := f.service.CancelWithdrawal(ctx, f.tenant, created.ID)
	if err != nil || cancelled.Status != domain.WithdrawalStatusCancelled {
		t.Fatalf("cancel=%+v err=%v, want cancelled", cancelled, err)
	}
	if _, err := f.service.CancelWithdrawal(ctx, f.tenant, created.ID); !errors.Is(err, domain.ErrWithdrawalInvalidState) {
		t.Fatalf("second cancel err=%v, want 409 sentinel", err)
	}
	wallet, sum, entries := withdrawalBalances(t, f)
	if wallet.AvailableBalance != 120000 || wallet.PendingBalance != 0 || !domain.WalletLedgerInvariant(&wallet, sum, 0) {
		t.Fatalf("cancel changed money: wallet=%+v sum=%d", wallet, sum)
	}
	if len(entries) != 3 || entries[1].Amount != -60000 || entries[2].Amount != 60000 || entries[2].EntryType != domain.LedgerEntryTypeRelease || entries[1].ReferenceID != entries[2].ReferenceID {
		t.Fatalf("ledger=%+v, want payment + hold + release with same reference", entries)
	}
}

// The status conditional update is the race backstop for a future admin
// processing step. This test moves the row to `processing` in a concurrent
// transaction while the tenant tries to cancel; exactly one transition wins,
// and a losing cancel must not return held money.
func TestKEL143CancelVersusProcessingHasOneWinner(t *testing.T) {
	db := openKEL143Database(t)
	f := seedWithdrawalFixture(t, db, 120000)
	ctx := context.Background()
	created, err := f.service.RequestWithdrawal(ctx, f.tenant, domain.RequestWithdrawalInput{Amount: 60000, IdempotencyKey: "race"})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var cancelErr, processingErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, cancelErr = f.service.CancelWithdrawal(ctx, f.tenant, created.ID)
	}()
	go func() {
		defer wg.Done()
		<-start
		processingErr = repository.NewTransactionManager(db).WithTransaction(ctx, func(ctx context.Context) error {
			// The future processor first locks the tenant wallet, matching
			// the request/cancel lock order, then claims the status only if
			// still requested. No payout is performed in KEL-143.
			if _, err := repository.NewWalletRepository(db).(repository.WalletLockingRepository).GetByTenantIDForUpdate(ctx, f.tenant); err != nil {
				return err
			}
			result := repository.GetDB(ctx, db).Model(&domain.Withdrawal{}).Where("id = ? AND tenant_id = ? AND status = ?", created.ID, f.tenant, domain.WithdrawalStatusRequested).Update("status", domain.WithdrawalStatusProcessing)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return domain.ErrWithdrawalInvalidState
			}
			return nil
		})
	}()
	close(start)
	wg.Wait()
	if !((cancelErr == nil && errors.Is(processingErr, domain.ErrWithdrawalInvalidState)) || (processingErr == nil && errors.Is(cancelErr, domain.ErrWithdrawalInvalidState))) {
		t.Fatalf("cancel err=%v processing err=%v, want exactly one winner", cancelErr, processingErr)
	}
	var stored domain.Withdrawal
	if err := db.First(&stored, "id = ?", created.ID).Error; err != nil {
		t.Fatal(err)
	}
	wallet, sum, entries := withdrawalBalances(t, f)
	if stored.Status == domain.WithdrawalStatusCancelled {
		if wallet.AvailableBalance != 120000 || wallet.PendingBalance != 0 || len(entries) != 3 || !domain.WalletLedgerInvariant(&wallet, sum, 0) {
			t.Fatalf("cancel won but hold not fully released: wallet=%+v sum=%d entries=%d", wallet, sum, len(entries))
		}
	} else if stored.Status == domain.WithdrawalStatusProcessing {
		if wallet.AvailableBalance != 60000 || wallet.PendingBalance != 60000 || len(entries) != 2 || !domain.WalletLedgerInvariant(&wallet, sum, 60000) {
			t.Fatalf("processing won but hold changed: wallet=%+v sum=%d entries=%d", wallet, sum, len(entries))
		}
	} else {
		t.Fatalf("unexpected status %q", stored.Status)
	}
}

// Failure paths: the configured minimum, no primary account, and amount
// above the available balance are rejected before any money is moved.
func TestKEL143RequestValidationDoesNotMoveMoney(t *testing.T) {
	db := openKEL143Database(t)
	f := seedWithdrawalFixture(t, db, 100000)
	ctx := context.Background()
	_, err := f.service.RequestWithdrawal(ctx, f.tenant, domain.RequestWithdrawalInput{Amount: 49999, IdempotencyKey: "below-min"})
	if !errors.Is(err, domain.ErrWithdrawalBelowMinimum) {
		t.Fatalf("below min err=%v", err)
	}
	_, err = f.service.RequestWithdrawal(ctx, f.tenant, domain.RequestWithdrawalInput{Amount: 110000, IdempotencyKey: "over-balance"})
	if !errors.Is(err, domain.ErrWithdrawalInsufficientBalance) {
		t.Fatalf("over balance err=%v", err)
	}
	if err := db.Model(&domain.BankAccount{}).Where("id = ?", f.account.ID).Update("is_primary", false).Error; err != nil {
		t.Fatal(err)
	}
	_, err = f.service.RequestWithdrawal(ctx, f.tenant, domain.RequestWithdrawalInput{Amount: 50000, IdempotencyKey: "no-primary"})
	if !errors.Is(err, domain.ErrWithdrawalNoPrimaryAccount) {
		t.Fatalf("no primary err=%v", err)
	}
	wallet, sum, entries := withdrawalBalances(t, f)
	if wallet.AvailableBalance != 100000 || wallet.PendingBalance != 0 || !domain.WalletLedgerInvariant(&wallet, sum, 0) || len(entries) != 1 {
		t.Fatalf("invalid requests moved money: wallet=%+v sum=%d entries=%d", wallet, sum, len(entries))
	}
}
