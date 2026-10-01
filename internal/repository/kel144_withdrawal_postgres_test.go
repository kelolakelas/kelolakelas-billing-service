package repository_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
)

func platformFixture(f *withdrawalFixture) usecase.PlatformWithdrawalUsecase {
	return usecase.NewPlatformWithdrawalUsecase(repository.NewWithdrawalRepository(f.db), repository.NewWalletRepository(f.db), repository.NewLedgerEntryRepository(f.db), repository.NewTransactionManager(f.db))
}

func TestKEL144ManualDecisionLedgerAndAudit(t *testing.T) {
	db := openKEL143Database(t)
	for _, decision := range []string{domain.WithdrawalStatusPaid, domain.WithdrawalStatusRejected} {
		t.Run(decision, func(t *testing.T) {
			f := seedWithdrawalFixture(t, db, 120000)
			ctx := context.Background()
			created, err := f.service.RequestWithdrawal(ctx, f.tenant, domain.RequestWithdrawalInput{Amount: 60000, IdempotencyKey: "manual"})
			if err != nil {
				t.Fatal(err)
			}
			admin := uuid.New()
			u := platformFixture(f)
			queue, err := u.ListRequested(ctx, domain.WithdrawalQuery{Page: 1, PageSize: 100})
			if err != nil || len(queue.Items) != 1 || queue.Items[0].AccountNumber != f.account.AccountNumber || queue.Items[0].TenantID != f.tenant {
				t.Fatalf("queue=%+v err=%v", queue, err)
			}
			result, err := u.Decide(ctx, admin, created.ID, decision, "bank-ref-"+uuid.NewString())
			if err != nil || result.Status != decision || result.DecidedBy == nil || *result.DecidedBy != admin || result.DecidedAt == nil {
				t.Fatalf("decision=%+v err=%v", result, err)
			}
			if _, err := u.Decide(ctx, admin, created.ID, decision, "other"); !errors.Is(err, domain.ErrWithdrawalInvalidState) {
				t.Fatalf("repeat=%v", err)
			}
			if _, err := f.service.CancelWithdrawal(ctx, f.tenant, created.ID); !errors.Is(err, domain.ErrWithdrawalInvalidState) {
				t.Fatalf("cancel after decision=%v", err)
			}
			wallet, sum, entries := withdrawalBalances(t, f)
			wantAvailable := int64(120000)
			if decision == domain.WithdrawalStatusPaid {
				wantAvailable = 60000
			}
			var externalSum int64
			if err := db.Model(&domain.LedgerEntry{}).Where("wallet_id = ? AND entry_type IN ?", f.wallet.ID, []string{domain.LedgerEntryTypePayment, domain.LedgerEntryTypePaid}).Select("COALESCE(SUM(amount), 0)").Scan(&externalSum).Error; err != nil {
				t.Fatal(err)
			}
			if wallet.AvailableBalance != wantAvailable || wallet.PendingBalance != 0 || !domain.WalletLedgerInvariant(&wallet, sum, 0) || wallet.AvailableBalance+wallet.PendingBalance != externalSum {
				t.Fatalf("wallet=%+v sum=%d entries=%+v", wallet, sum, entries)
			}
			count := 3
			if decision == domain.WithdrawalStatusPaid {
				count = 4
			}
			if len(entries) != count {
				t.Fatalf("ledger entries=%d, want %d", len(entries), count)
			}
			history, err := f.service.GetWithdrawal(ctx, f.tenant, created.ID)
			if err != nil || history.AccountNumber == f.account.AccountNumber {
				t.Fatalf("tenant history exposed account: %+v %v", history, err)
			}
		})
	}
}

func TestKEL144DuplicateReferenceRollsBackDecision(t *testing.T) {
	db := openKEL143Database(t)
	first := seedWithdrawalFixture(t, db, 120000)
	second := seedWithdrawalFixture(t, db, 120000)
	ctx := context.Background()
	a, err := first.service.RequestWithdrawal(ctx, first.tenant, domain.RequestWithdrawalInput{Amount: 60000, IdempotencyKey: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.service.RequestWithdrawal(ctx, second.tenant, domain.RequestWithdrawalInput{Amount: 60000, IdempotencyKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platformFixture(first).Decide(ctx, uuid.New(), a.ID, domain.WithdrawalStatusPaid, "shared-reference"); err != nil {
		t.Fatal(err)
	}
	if _, err := platformFixture(second).Decide(ctx, uuid.New(), b.ID, domain.WithdrawalStatusPaid, "shared-reference"); !errors.Is(err, domain.ErrWithdrawalDuplicateReference) {
		t.Fatalf("duplicate reference=%v", err)
	}
	wallet, sum, entries := withdrawalBalances(t, second)
	if wallet.AvailableBalance != 60000 || wallet.PendingBalance != 60000 || !domain.WalletLedgerInvariant(&wallet, sum, 60000) || len(entries) != 2 {
		t.Fatalf("failed decision changed ledger: wallet=%+v sum=%d entries=%d", wallet, sum, len(entries))
	}
	pending, err := second.service.GetWithdrawal(ctx, second.tenant, b.ID)
	if err != nil || pending.Status != domain.WithdrawalStatusRequested {
		t.Fatalf("rollback status=%+v err=%v", pending, err)
	}
}

func TestKEL144ConcurrentDecisionAndCancelOneWinner(t *testing.T) {
	db := openKEL143Database(t)
	for _, raceCancel := range []bool{false, true} {
		f := seedWithdrawalFixture(t, db, 120000)
		ctx := context.Background()
		w, err := f.service.RequestWithdrawal(ctx, f.tenant, domain.RequestWithdrawalInput{Amount: 60000, IdempotencyKey: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := platformFixture(f).Decide(ctx, uuid.New(), w.ID, domain.WithdrawalStatusPaid, uuid.NewString())
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			if raceCancel {
				_, err := f.service.CancelWithdrawal(ctx, f.tenant, w.ID)
				errs <- err
			} else {
				_, err := platformFixture(f).Decide(ctx, uuid.New(), w.ID, domain.WithdrawalStatusRejected, "reason")
				errs <- err
			}
		}()
		close(start)
		wg.Wait()
		close(errs)
		success, conflict := 0, 0
		for err := range errs {
			if err == nil {
				success++
			} else if errors.Is(err, domain.ErrWithdrawalInvalidState) {
				conflict++
			} else {
				t.Fatalf("race err=%v", err)
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatalf("raceCancel=%v success=%d conflict=%d", raceCancel, success, conflict)
		}
		wallet, sum, _ := withdrawalBalances(t, f)
		if wallet.PendingBalance != 0 || wallet.AvailableBalance != sum {
			t.Fatalf("wallet=%+v sum=%d", wallet, sum)
		}
	}
}
