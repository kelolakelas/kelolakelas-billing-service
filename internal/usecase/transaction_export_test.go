package usecase

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// exportRepoStub is an in-memory transaction store backing both the summary
// and the export, so the test proves the two agree on the same rows.
type exportRepoStub struct {
	transactionRepoStub
	rows       []domain.Transaction
	iterateErr error
	batches    int
}

func (r *exportRepoStub) SummarizePaid(_ context.Context, tenantID uuid.UUID, from, until time.Time) ([]domain.SalesSummary, error) {
	byCurrency := map[string]*domain.SalesSummary{}
	for i := range r.rows {
		tx := &r.rows[i]
		if tx.TenantID != tenantID || tx.Status != domain.TransactionStatusPaid || tx.PaidAt == nil {
			continue
		}
		if tx.PaidAt.Before(from) || !tx.PaidAt.Before(until) {
			continue
		}
		bucket, ok := byCurrency[tx.Currency]
		if !ok {
			bucket = &domain.SalesSummary{Currency: tx.Currency}
			byCurrency[tx.Currency] = bucket
		}
		bucket.TransactionCount++
		bucket.GrossAmount += tx.GrossAmount
		bucket.NetAmount += tx.NetAmount
	}
	var totals []domain.SalesSummary
	for _, bucket := range byCurrency {
		totals = append(totals, *bucket)
	}
	if totals == nil {
		totals = []domain.SalesSummary{}
	}
	return totals, nil
}

func (r *exportRepoStub) IterateExport(_ context.Context, tenantID uuid.UUID, query domain.TransactionQuery, batchSize int, fn func([]domain.Transaction) error) error {
	if r.iterateErr != nil {
		return r.iterateErr
	}
	var filtered []domain.Transaction
	for i := range r.rows {
		tx := r.rows[i]
		if tx.TenantID != tenantID {
			continue
		}
		if query.Status != "" && tx.Status != query.Status {
			continue
		}
		if query.DateFrom != nil && (tx.PaidAt == nil || tx.PaidAt.Before(*query.DateFrom)) {
			continue
		}
		if query.DateTo != nil && (tx.PaidAt == nil || !tx.PaidAt.Before(query.DateTo.AddDate(0, 0, 1))) {
			continue
		}
		filtered = append(filtered, tx)
	}
	if batchSize <= 0 {
		batchSize = 500
	}
	for start := 0; start < len(filtered); start += batchSize {
		end := start + batchSize
		if end > len(filtered) {
			end = len(filtered)
		}
		r.batches++
		if err := fn(filtered[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func exportTestUsecase(repo *exportRepoStub) TransactionUsecase {
	return newTransactionUsecase(repo, nil, nil, nil, nil, nil, expiryTestConfig(), nil, nil)
}

func paidExportRow(tenant uuid.UUID, currency string, gross, net int64, paidAt time.Time) domain.Transaction {
	return domain.Transaction{
		ID: uuid.New(), MerchantOrderID: "kel147-" + uuid.NewString(), TenantID: tenant,
		ParentID: uuid.New(), StudentID: uuid.New(), EnrollmentID: uuid.New(),
		SubtotalAmount: gross, GrossAmount: gross, PlatformFee: gross - net, NetAmount: net,
		Currency: currency, Status: domain.TransactionStatusPaid,
		PaidAt: &paidAt, CreatedAt: paidAt.Add(-time.Hour),
	}
}

func parseExportCSV(t *testing.T, raw string) [][]string {
	t.Helper()
	records, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("export is not valid CSV: %v\n%s", err, raw)
	}
	return records
}

func sumNetByCurrency(records [][]string) map[string]int64 {
	sums := map[string]int64{}
	var currencyIdx, netIdx = -1, -1
	for i, name := range records[0] {
		switch name {
		case "currency":
			currencyIdx = i
		case "net":
			netIdx = i
		}
	}
	for _, record := range records[1:] {
		net, _ := strconv.ParseInt(record[netIdx], 10, 64)
		sums[record[currencyIdx]] += net
	}
	return sums
}

// The core KEL-147 invariant at the usecase level: the CSV net totals per
// currency equal the summary totals for the same tenant and paid-date range,
// and different currencies are never summed together.
func TestExportTransactionsMatchesSalesSummary(t *testing.T) {
	tenant, other := uuid.New(), uuid.New()
	from := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
	until := to.AddDate(0, 0, 1)
	outside := from.Add(-time.Hour)
	repo := &exportRepoStub{rows: []domain.Transaction{
		paidExportRow(tenant, "IDR", 100000, 90000, from),
		paidExportRow(tenant, "IDR", 250000, 225000, from.AddDate(0, 0, 14)),
		paidExportRow(tenant, "IDR", 50000, 45000, until.Add(-time.Microsecond)),
		paidExportRow(tenant, "USD", 20, 18, from.AddDate(0, 0, 3)),
		// Excluded from both: outside the range, another tenant, unpaid.
		paidExportRow(tenant, "IDR", 7000, 6000, outside),
		paidExportRow(other, "IDR", 900000, 800000, from.AddDate(0, 0, 2)),
	}}
	repo.rows = append(repo.rows, domain.Transaction{
		ID: uuid.New(), MerchantOrderID: "kel147-unpaid", TenantID: tenant,
		ParentID: uuid.New(), StudentID: uuid.New(), EnrollmentID: uuid.New(),
		SubtotalAmount: 12000, GrossAmount: 12000, NetAmount: 11000,
		Currency: "IDR", Status: domain.TransactionStatusPending,
		CreatedAt: from.AddDate(0, 0, 5),
	})
	u := exportTestUsecase(repo)

	var buf bytes.Buffer
	query := domain.TransactionQuery{Status: domain.TransactionStatusPaid, DateBy: domain.TransactionDateByPaidAt, DateFrom: &from, DateTo: &to}
	got, err := u.ExportTransactions(context.Background(), tenant, query, &buf)
	if err != nil {
		t.Fatalf("ExportTransactions: %v", err)
	}
	if got != 4 {
		t.Fatalf("rows=%d, want the 4 in-range paid rows", got)
	}
	records := parseExportCSV(t, buf.String())
	if len(records) != 5 {
		t.Fatalf("records=%d, want header + 4 rows", len(records))
	}
	totals, err := u.SalesSummary(context.Background(), tenant, from, until)
	if err != nil {
		t.Fatalf("SalesSummary: %v", err)
	}
	want := map[string]int64{}
	for _, bucket := range totals {
		want[bucket.Currency] = bucket.NetAmount
	}
	sums := sumNetByCurrency(records)
	if len(sums) != len(want) {
		t.Fatalf("csv currencies=%v, summary=%v, want the same buckets", sums, want)
	}
	for currency, net := range want {
		if sums[currency] != net {
			t.Fatalf("csv net %s=%d, summary=%d", currency, sums[currency], net)
		}
	}
	if repo.batches != 1 {
		t.Fatalf("batches=%d, want one batch for 4 rows", repo.batches)
	}
}

func TestExportTransactionsStreamsInBatches(t *testing.T) {
	tenant := uuid.New()
	from := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
	repo := &exportRepoStub{}
	for i := 0; i < 1203; i++ {
		repo.rows = append(repo.rows, paidExportRow(tenant, "IDR", 10000, 9000, from.Add(time.Duration(i)*time.Minute)))
	}
	u := exportTestUsecase(repo)

	var buf bytes.Buffer
	query := domain.TransactionQuery{Status: domain.TransactionStatusPaid, DateBy: domain.TransactionDateByPaidAt, DateFrom: &from, DateTo: &to}
	got, err := u.ExportTransactions(context.Background(), tenant, query, &buf)
	if err != nil {
		t.Fatalf("ExportTransactions: %v", err)
	}
	if got != 1203 {
		t.Fatalf("rows=%d, want 1203", got)
	}
	if repo.batches != 3 {
		t.Fatalf("batches=%d, want 3 batches of 500", repo.batches)
	}
	records := parseExportCSV(t, buf.String())
	if len(records) != 1204 {
		t.Fatalf("records=%d, want header + 1203 rows", len(records))
	}
}

func TestExportTransactionsEmptyRangeYieldsHeaderOnly(t *testing.T) {
	tenant := uuid.New()
	from := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
	u := exportTestUsecase(&exportRepoStub{})

	var buf bytes.Buffer
	query := domain.TransactionQuery{Status: domain.TransactionStatusPaid, DateBy: domain.TransactionDateByPaidAt, DateFrom: &from, DateTo: &to}
	got, err := u.ExportTransactions(context.Background(), tenant, query, &buf)
	if err != nil {
		t.Fatalf("ExportTransactions: %v", err)
	}
	if got != 0 {
		t.Fatalf("rows=%d, want 0", got)
	}
	records := parseExportCSV(t, buf.String())
	if len(records) != 1 {
		t.Fatalf("records=%v, want header only", records)
	}
	for i, name := range domain.TransactionExportColumns {
		if records[0][i] != name {
			t.Fatalf("header[%d]=%q, want %q", i, records[0][i], name)
		}
	}
}

func TestExportTransactionsReportsRepositoryFailure(t *testing.T) {
	u := exportTestUsecase(&exportRepoStub{iterateErr: errors.New("db down")})

	var buf bytes.Buffer
	from := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
	query := domain.TransactionQuery{Status: domain.TransactionStatusPaid, DateBy: domain.TransactionDateByPaidAt, DateFrom: &from, DateTo: &to}
	if _, err := u.ExportTransactions(context.Background(), uuid.New(), query, &buf); err == nil {
		t.Fatal("want the repository error, got nil")
	}
}
