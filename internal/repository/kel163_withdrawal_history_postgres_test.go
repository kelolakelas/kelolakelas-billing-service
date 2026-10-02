package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

func TestKEL163DecisionHistoryPostgres(t *testing.T) {
	db := openKEL143Database(t)
	if err := db.Exec("DELETE FROM withdrawals").Error; err != nil {
		t.Fatal(err)
	}
	f := seedWithdrawalFixture(t, db, 0)
	ctx := context.Background()
	r := repository.NewWithdrawalRepository(db)
	admin := uuid.New()
	base := time.Now().UTC().Truncate(time.Second)
	makeRow := func(status string, at *time.Time, id uuid.UUID) domain.Withdrawal {
		t.Helper()
		w := domain.Withdrawal{ID: id, TenantID: f.tenant, BankAccountID: f.account.ID, Amount: 60000, NetAmount: 60000, Status: status, RequestedAt: base.Add(-time.Hour), BankCodeSnapshot: f.account.BankCode, AccountNumberSnapshot: f.account.AccountNumber, AccountNameSnapshot: f.account.AccountName, DecidedAt: at}
		if at != nil {
			w.DecidedBy = &admin
			if status == domain.WithdrawalStatusPaid {
				ref := uuid.NewString()
				w.TransferReference = &ref
			} else {
				reason := "rejected by operator"
				w.RejectReason = &reason
			}
		}
		if err := db.Create(&w).Error; err != nil {
			t.Fatal(err)
		}
		return w
	}
	empty, err := platformFixture(f).ListDecided(ctx, domain.WithdrawalQuery{Page: 1, PageSize: 2})
	if err != nil || len(empty.Items) != 0 || empty.Pagination.TotalItems != 0 {
		t.Fatalf("empty history=%+v err=%v", empty, err)
	}
	makeRow(domain.WithdrawalStatusRequested, nil, uuid.New())
	makeRow(domain.WithdrawalStatusCancelled, nil, uuid.New())
	old := makeRow(domain.WithdrawalStatusRejected, &base, uuid.New())
	// Explicit per-row timestamps: wall-clock truncation must not sneak equal
	// decided_at values into the order check.
	atA := base.Add(time.Hour)
	atB := base.Add(2 * time.Hour)
	atC := base.Add(3 * time.Hour)
	rowA := makeRow(domain.WithdrawalStatusPaid, &atA, uuid.New())
	rowB := makeRow(domain.WithdrawalStatusRejected, &atB, uuid.New())
	other := seedWithdrawalFixture(t, db, 0)
	crossRef := uuid.NewString()
	cross := domain.Withdrawal{ID: uuid.New(), TenantID: other.tenant, BankAccountID: other.account.ID, Amount: 70000, NetAmount: 70000, Status: domain.WithdrawalStatusPaid, RequestedAt: base, DecidedAt: &atC, DecidedBy: &admin, TransferReference: &crossRef, BankCodeSnapshot: other.account.BankCode, AccountNumberSnapshot: other.account.AccountNumber, AccountNameSnapshot: other.account.AccountName}
	if err := db.Create(&cross).Error; err != nil {
		t.Fatal(err)
	}
	// Equal timestamps still happen across tenants in production, so the newest
	// pair shares one timestamp and the UUID tie-break decides their order.
	tie := base.Add(4 * time.Hour)
	low, high := uuid.New(), uuid.New()
	if low.String() > high.String() {
		low, high = high, low
	}
	makeRow(domain.WithdrawalStatusPaid, &tie, low)
	makeRow(domain.WithdrawalStatusRejected, &tie, high)
	// Newest first: the equal-timestamp pair (UUID descending), then the
	// explicit per-row timestamps, then the oldest decision.
	expected := []uuid.UUID{high, low, cross.ID, rowB.ID, rowA.ID, old.ID}
	var all []domain.PlatformWithdrawalResponse
	var first *domain.PlatformWithdrawalListResponse
	for page := 1; page <= 3; page++ {
		got, err := platformFixture(f).ListDecided(ctx, domain.WithdrawalQuery{Page: page, PageSize: 2})
		if err != nil {
			t.Fatal(err)
		}
		if page == 1 {
			first = got
		}
		if len(got.Items) != 2 {
			t.Fatalf("page %d items=%d, want 2", page, len(got.Items))
		}
		all = append(all, got.Items...)
	}
	if first.Pagination.TotalItems != 6 || first.Pagination.TotalPages != 3 {
		t.Fatalf("pagination=%+v, want total 6 across 3 pages", first.Pagination)
	}
	for i, id := range expected {
		if all[i].ID != id || all[i].DecidedAt == nil || all[i].DecidedBy == nil || all[i].AccountNumber == "" || all[i].TenantID == uuid.Nil || (all[i].TransferReference == nil && all[i].RejectReason == nil) {
			t.Fatalf("item %d=%+v want id=%s", i, all[i], id)
		}
	}
	if all[2].TenantID == f.tenant || all[2].TenantID != other.tenant {
		t.Fatalf("cross-tenant decision tenant=%s, want %s", all[2].TenantID, other.tenant)
	}
	queue, total, err := r.ListRequested(ctx, 1, 20)
	if err != nil || total != 1 || len(queue) != 1 || queue[0].Status != domain.WithdrawalStatusRequested {
		t.Fatalf("unchanged queue=%+v total=%d err=%v", queue, total, err)
	}
}
