package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type fakeWallets struct {
	wallet *domain.Wallet
	err    error
}

func (f *fakeWallets) GetByTenantID(context.Context, uuid.UUID) (*domain.Wallet, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.wallet, nil
}

func (f *fakeWallets) Create(context.Context, *domain.Wallet) error { return nil }
func (f *fakeWallets) Update(context.Context, *domain.Wallet) error { return nil }

type fakeLedger struct {
	entries []domain.LedgerEntry
	total   int64
	err     error
}

func (f *fakeLedger) Create(context.Context, *domain.LedgerEntry) error { return nil }
func (f *fakeLedger) GetByID(context.Context, uuid.UUID) (*domain.LedgerEntry, error) {
	return nil, gorm.ErrRecordNotFound
}
func (f *fakeLedger) ListByWallet(context.Context, uuid.UUID, int, int) ([]domain.LedgerEntry, int64, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.entries, f.total, nil
}
func (f *fakeLedger) SumByWallet(context.Context, uuid.UUID) (int64, error) { return 0, nil }

// KEL-142: a tenant without a wallet gets a zero balance, not an error.
func TestWalletBalanceWithoutWalletIsZero(t *testing.T) {
	u := NewWalletUsecase(&fakeWallets{err: gorm.ErrRecordNotFound}, &fakeLedger{})
	balance, err := u.GetBalance(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance.AvailableBalance != 0 || balance.PendingBalance != 0 {
		t.Fatalf("balance=%+v, want zeros", balance)
	}
}

func TestWalletBalanceMirrorsStoredRow(t *testing.T) {
	tenant := uuid.New()
	u := NewWalletUsecase(&fakeWallets{wallet: &domain.Wallet{ID: uuid.New(), TenantID: tenant, AvailableBalance: 120000, PendingBalance: 5000}}, &fakeLedger{})
	balance, err := u.GetBalance(context.Background(), tenant)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance.AvailableBalance != 120000 || balance.PendingBalance != 5000 {
		t.Fatalf("balance=%+v, want the stored row", balance)
	}
}

// KEL-142: a tenant without a wallet gets an empty ledger page, not an error.
func TestLedgerWithoutWalletIsEmpty(t *testing.T) {
	u := NewWalletUsecase(&fakeWallets{err: gorm.ErrRecordNotFound}, &fakeLedger{})
	page, err := u.ListLedger(context.Background(), uuid.New(), domain.LedgerQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListLedger: %v", err)
	}
	if len(page.Items) != 0 || page.Pagination.TotalItems != 0 {
		t.Fatalf("page=%+v, want empty", page)
	}
	if page.Pagination.Page != 1 || page.Pagination.PageSize != 20 {
		t.Fatalf("pagination=%+v, want the requested window", page.Pagination)
	}
}

func TestLedgerListsNewestFirstWindow(t *testing.T) {
	walletID := uuid.New()
	entry := func() domain.LedgerEntry {
		return domain.LedgerEntry{ID: uuid.New(), WalletID: walletID, ReferenceID: uuid.New(), ReferenceType: "transaction", Amount: 90000, EntryType: "payment_received"}
	}
	u := NewWalletUsecase(&fakeWallets{wallet: &domain.Wallet{ID: walletID, TenantID: uuid.New()}}, &fakeLedger{entries: []domain.LedgerEntry{entry(), entry()}, total: 7})
	page, err := u.ListLedger(context.Background(), uuid.New(), domain.LedgerQuery{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("ListLedger: %v", err)
	}
	if len(page.Items) != 2 || page.Pagination.TotalItems != 7 || page.Pagination.TotalPages != 4 {
		t.Fatalf("page=%+v, want 2 items of 7 over 4 pages", page)
	}
}
