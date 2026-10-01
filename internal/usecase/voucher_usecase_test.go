package usecase

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

// fakeVouchers is an in-memory repository.VoucherRepository. Create enforces
// the tenant+code uniqueness backstop with the same constraint text Postgres
// reports, so the duplicate mapping is exercised without a database.
type fakeVouchers struct {
	rows       map[uuid.UUID]*domain.Voucher
	createErr  error
	createHits int
	updateHits int
	deleteHits int
}

func newFakeVouchers(seed ...*domain.Voucher) *fakeVouchers {
	f := &fakeVouchers{rows: map[uuid.UUID]*domain.Voucher{}}
	for _, v := range seed {
		cp := *v
		f.rows[v.ID] = &cp
	}
	return f
}

var _ repository.VoucherRepository = (*fakeVouchers)(nil)

func (f *fakeVouchers) LockTenantCodes(context.Context, uuid.UUID) error { return nil }

func (f *fakeVouchers) Create(_ context.Context, voucher *domain.Voucher) error {
	f.createHits++
	if f.createErr != nil {
		return f.createErr
	}
	for _, existing := range f.rows {
		if existing.TenantID == voucher.TenantID && existing.Code == voucher.Code {
			return errors.New(`pq: duplicate key value violates unique constraint "uq_vouchers_tenant_code"`)
		}
	}
	cp := *voucher
	f.rows[voucher.ID] = &cp
	return nil
}

func (f *fakeVouchers) GetByIDScoped(_ context.Context, tenantID, id uuid.UUID) (*domain.Voucher, error) {
	v, ok := f.rows[id]
	if !ok || v.TenantID != tenantID {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *v
	return &cp, nil
}

func (f *fakeVouchers) GetByCode(_ context.Context, tenantID uuid.UUID, code string) (*domain.Voucher, error) {
	for _, v := range f.rows {
		if v.TenantID == tenantID && equalFold(v.Code, code) {
			cp := *v
			return &cp, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (f *fakeVouchers) HasCodeOtherThan(_ context.Context, tenantID, excludeID uuid.UUID, code string) (bool, error) {
	for _, v := range f.rows {
		if v.TenantID == tenantID && v.ID != excludeID && v.DeletedAt.Time.IsZero() && equalFold(v.Code, code) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeVouchers) GetByIDScopedForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Voucher, error) {
	return f.GetByIDScoped(ctx, tenantID, id)
}

func (f *fakeVouchers) ListByTenant(_ context.Context, tenantID uuid.UUID, page, pageSize int) ([]domain.Voucher, int64, error) {
	var rows []domain.Voucher
	for _, v := range f.rows {
		if v.TenantID == tenantID {
			rows = append(rows, *v)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	total := int64(len(rows))
	start := (page - 1) * pageSize
	if start >= len(rows) {
		return nil, total, nil
	}
	end := start + pageSize
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end], total, nil
}

func (f *fakeVouchers) Update(_ context.Context, voucher *domain.Voucher) error {
	f.updateHits++
	if _, ok := f.rows[voucher.ID]; !ok {
		return gorm.ErrRecordNotFound
	}
	cp := *voucher
	f.rows[voucher.ID] = &cp
	return nil
}

func (f *fakeVouchers) SoftDelete(_ context.Context, tenantID, id uuid.UUID) (bool, error) {
	f.deleteHits++
	v, ok := f.rows[id]
	if !ok || v.TenantID != tenantID {
		return false, nil
	}
	delete(f.rows, id)
	return true, nil
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		// Still compare case-insensitively without strings to keep the fake
		// dependency-free; length differs means unequal here.
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'a' <= ca && ca <= 'z' {
			ca -= 'a' - 'A'
		}
		if 'a' <= cb && cb <= 'z' {
			cb -= 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func voucherSeed(tenant uuid.UUID, code string, currentUses int) *domain.Voucher {
	now := time.Now()
	return &domain.Voucher{
		ID: uuid.New(), TenantID: tenant, Code: code,
		DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10,
		CurrentUses: currentUses, IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
}

func intPtr(n int) *int { return &n }

// KEL-161: create stores the canonical code and maps the unique backstop to a
// duplicate; invalid payloads never reach the store.
func TestVoucherCreateStoresCanonicalCode(t *testing.T) {
	tenant := uuid.New()
	fake := newFakeVouchers()
	u := NewVoucherUsecase(fake, nil)

	got, err := u.Create(context.Background(), tenant, &domain.CreateVoucherRequest{
		Code: "  hemat10 ", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Code != "HEMAT10" || !got.IsActive {
		t.Fatalf("created=%+v, want canonical HEMAT10 active", got)
	}
	if fake.createHits != 1 {
		t.Fatalf("createHits=%d, want 1", fake.createHits)
	}
}

func TestVoucherCreateDuplicateMapsConstraint(t *testing.T) {
	tenant := uuid.New()
	fake := newFakeVouchers(voucherSeed(tenant, "HEMAT10", 0))
	u := NewVoucherUsecase(fake, nil)

	// Same code in different case must still collide on the backstop.
	_, err := u.Create(context.Background(), tenant, &domain.CreateVoucherRequest{
		Code: "hemat10", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10,
	})
	if !errors.Is(err, domain.ErrVoucherDuplicate) {
		t.Fatalf("err=%v, want ErrVoucherDuplicate", err)
	}
}

func TestVoucherCreateRejectsLegacyMixedCaseWithoutWriting(t *testing.T) {
	tenant, other := uuid.New(), uuid.New()
	fake := newFakeVouchers(voucherSeed(tenant, "Legacy10", 0))
	u := NewVoucherUsecase(fake, nil)
	req := &domain.CreateVoucherRequest{Code: "legacy10", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10}
	if _, err := u.Create(context.Background(), tenant, req); !errors.Is(err, domain.ErrVoucherDuplicate) {
		t.Fatalf("legacy collision err=%v, want ErrVoucherDuplicate", err)
	}
	if fake.createHits != 0 {
		t.Fatalf("createHits=%d, want no insert on collision", fake.createHits)
	}
	if _, err := u.Create(context.Background(), other, req); err != nil {
		t.Fatalf("other tenant create: %v", err)
	}
}

func TestVoucherCreateInvalidNeverStores(t *testing.T) {
	tenant := uuid.New()
	fake := newFakeVouchers()
	u := NewVoucherUsecase(fake, nil)

	for name, req := range map[string]*domain.CreateVoucherRequest{
		"nil request":     nil,
		"percentage zero": {Code: "A", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 0},
		"percentage over": {Code: "A", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 101},
		"nominal zero":    {Code: "A", DiscountType: domain.VoucherDiscountFixedAmount, DiscountValue: 0},
		"unknown type":    {Code: "A", DiscountType: "bogus", DiscountValue: 10},
		"blank code":      {Code: "  ", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10},
		"inverted range":  {Code: "A", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10, ValidFrom: voucherTimePtr(time.Now().Add(time.Hour)), ValidUntil: voucherTimePtr(time.Now())},
		"max uses zero":   {Code: "A", DiscountType: domain.VoucherDiscountPercentage, DiscountValue: 10, MaxUses: intPtr(0)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := u.Create(context.Background(), tenant, req); !errors.Is(err, domain.ErrVoucherInvalid) {
				t.Fatalf("err=%v, want ErrVoucherInvalid", err)
			}
		})
	}
	if fake.createHits != 0 {
		t.Fatalf("createHits=%d, want no store call", fake.createHits)
	}
}

// KEL-161: cross-tenant reads are not-found; the usage count rides along for
// the list screen.
func TestVoucherGetIsTenantScoped(t *testing.T) {
	tenant, other := uuid.New(), uuid.New()
	seed := voucherSeed(tenant, "HEMAT10", 2)
	u := NewVoucherUsecase(newFakeVouchers(seed), nil)

	got, err := u.Get(context.Background(), tenant, seed.ID)
	if err != nil || got.CurrentUses != 2 {
		t.Fatalf("got=%+v err=%v, want usage count 2", got, err)
	}
	if _, err := u.Get(context.Background(), other, seed.ID); !errors.Is(err, domain.ErrVoucherNotFound) {
		t.Fatalf("cross-tenant err=%v, want ErrVoucherNotFound", err)
	}
	if _, err := u.Get(context.Background(), tenant, uuid.New()); !errors.Is(err, domain.ErrVoucherNotFound) {
		t.Fatalf("missing err=%v, want ErrVoucherNotFound", err)
	}
}

// KEL-161: rename collisions, usage-cap guards, and expired reactivation.
func TestVoucherUpdateGuards(t *testing.T) {
	tenant := uuid.New()
	first, second := voucherSeed(tenant, "FIRST", 0), voucherSeed(tenant, "SECOND", 3)
	u := NewVoucherUsecase(newFakeVouchers(first, second), nil)

	rename := "second"
	if _, err := u.Update(context.Background(), tenant, first.ID, &domain.UpdateVoucherRequest{Code: &rename}); !errors.Is(err, domain.ErrVoucherDuplicate) {
		t.Fatalf("rename err=%v, want ErrVoucherDuplicate", err)
	}

	cap := 2
	if _, err := u.Update(context.Background(), tenant, second.ID, &domain.UpdateVoucherRequest{MaxUses: &cap}); !errors.Is(err, domain.ErrVoucherInvalid) {
		t.Fatalf("lowered cap err=%v, want ErrVoucherInvalid", err)
	}

	if _, err := u.Update(context.Background(), tenant, first.ID, &domain.UpdateVoucherRequest{}); !errors.Is(err, domain.ErrVoucherInvalid) {
		t.Fatalf("empty patch err=%v, want ErrVoucherInvalid", err)
	}

	// Reactivating an expired voucher is allowed: only the supplied fields
	// change and no validity check runs on the toggle.
	past := time.Now().Add(-2 * time.Hour)
	older := past.Add(-time.Hour)
	expired := voucherSeed(tenant, "OLD", 1)
	expired.ValidFrom, expired.ValidUntil, expired.IsActive = &older, &past, false
	u2 := NewVoucherUsecase(newFakeVouchers(expired), nil)
	active := true
	got, err := u2.Update(context.Background(), tenant, expired.ID, &domain.UpdateVoucherRequest{IsActive: &active})
	if err != nil || !got.IsActive {
		t.Fatalf("reactivate got=%+v err=%v, want active", got, err)
	}
}

// KEL-161: a voucher that already discounted a transaction can only be
// deactivated, never deleted.
func TestVoucherDeleteRefusesUsed(t *testing.T) {
	tenant, other := uuid.New(), uuid.New()
	unused, used := voucherSeed(tenant, "FRESH", 0), voucherSeed(tenant, "SPENT", 1)
	fake := newFakeVouchers(unused, used)
	u := NewVoucherUsecase(fake, nil)

	if err := u.Delete(context.Background(), tenant, unused.ID); err != nil {
		t.Fatalf("delete unused: %v", err)
	}
	if err := u.Delete(context.Background(), tenant, used.ID); !errors.Is(err, domain.ErrVoucherInUse) {
		t.Fatalf("delete used err=%v, want ErrVoucherInUse", err)
	}
	if _, err := fake.GetByIDScoped(context.Background(), tenant, used.ID); err != nil {
		t.Fatalf("used voucher must survive a refused delete: %v", err)
	}
	if err := u.Delete(context.Background(), other, used.ID); !errors.Is(err, domain.ErrVoucherNotFound) {
		t.Fatalf("cross-tenant delete err=%v, want ErrVoucherNotFound", err)
	}
}

func TestVoucherListIsTenantScoped(t *testing.T) {
	tenant, other := uuid.New(), uuid.New()
	u := NewVoucherUsecase(newFakeVouchers(
		voucherSeed(tenant, "A", 0),
		voucherSeed(tenant, "B", 5),
		voucherSeed(other, "C", 0),
	), nil)

	page, err := u.List(context.Background(), tenant, domain.VoucherQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Pagination.TotalItems != 2 || len(page.Items) != 2 {
		t.Fatalf("page=%+v, want 2 tenant rows", page)
	}
	for _, item := range page.Items {
		if item.Code != "A" && item.Code != "B" {
			t.Fatalf("item=%+v, want only tenant rows", item)
		}
	}
	foreign, err := u.List(context.Background(), other, domain.VoucherQuery{})
	if err != nil || foreign.Pagination.TotalItems != 1 {
		t.Fatalf("foreign=%+v err=%v, want 1 row", foreign, err)
	}
}

func voucherTimePtr(t time.Time) *time.Time { return &t }
