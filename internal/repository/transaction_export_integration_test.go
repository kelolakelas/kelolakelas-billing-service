package repository

import (
	"bytes"
	"context"
	"encoding/csv"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// KEL-147 AC1 against PostgreSQL: the CSV net totals per currency equal the
// summary totals for the same tenant and paid-date range, and neither the
// list's created_at default nor any other tenant/status leaks in.
func TestIterateExportMatchesSummarizePaidPostgres(t *testing.T) {
	db := openSalesSummaryDatabase(t)
	repo := NewTransactionRepository(db).(*transactionRepository)
	tenant, otherTenant := uuid.New(), uuid.New()
	from := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	to := until.AddDate(0, 0, -1)

	seedSalesTransactions(t, db, []seededTransaction{
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 100000, net: 90000, paidAt: at(from)},
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 250000, net: 225000, paidAt: at(from.AddDate(0, 0, 14))},
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 50000, net: 45000, paidAt: at(until.Add(-time.Microsecond))},
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "USD", gross: 20, net: 18, paidAt: at(from.AddDate(0, 0, 3))},
		// Late-paid after a local expiry still counts: only paid_at and the
		// paid status decide, never the row's history.
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 60000, net: 54000, paidAt: at(from.AddDate(0, 0, 20))},
		// Not counted: outside the range, another tenant, unpaid, deleted.
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 7000, net: 6000, paidAt: at(from.Add(-time.Microsecond))},
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 8000, net: 7000, paidAt: at(until)},
		{tenant: otherTenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 900000, net: 800000, paidAt: at(from.AddDate(0, 0, 2))},
		{tenant: tenant, status: domain.TransactionStatusPending, currency: "IDR", gross: 12000, net: 11000},
		{tenant: tenant, status: domain.TransactionStatusPaid, currency: "IDR", gross: 16000, net: 15000, paidAt: at(from.AddDate(0, 0, 6)), deleted: true},
	})

	query := domain.TransactionQuery{
		Status: domain.TransactionStatusPaid, DateBy: domain.TransactionDateByPaidAt,
		DateFrom: &from, DateTo: &to,
	}
	var streamed []domain.Transaction
	if err := repo.IterateExport(context.Background(), tenant, query, 2, func(batch []domain.Transaction) error {
		streamed = append(streamed, batch...)
		return nil
	}); err != nil {
		t.Fatalf("IterateExport: %v", err)
	}
	if len(streamed) != 5 {
		t.Fatalf("streamed=%d rows, want the 5 in-range paid rows of this tenant", len(streamed))
	}
	// Batching must not reorder: oldest paid_at first.
	for i := 1; i < len(streamed); i++ {
		if streamed[i].PaidAt.Before(*streamed[i-1].PaidAt) {
			t.Fatal("export rows are not chronological")
		}
	}

	var buf bytes.Buffer
	writer := domain.NewTransactionExportWriter(&buf)
	if err := domain.WriteTransactionExportHeader(writer); err != nil {
		t.Fatal(err)
	}
	for i := range streamed {
		if err := domain.WriteTransactionExportRow(writer, streamed[i]); err != nil {
			t.Fatal(err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("export is not valid CSV: %v", err)
	}
	if len(records) != 6 {
		t.Fatalf("records=%d, want header + 5 rows", len(records))
	}
	sums := map[string]int64{}
	counts := map[string]int64{}
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
		net, err := strconv.ParseInt(record[netIdx], 10, 64)
		if err != nil {
			t.Fatalf("net=%q is not an integer: %v", record[netIdx], err)
		}
		sums[record[currencyIdx]] += net
		counts[record[currencyIdx]]++
	}
	totals, err := repo.SummarizePaid(context.Background(), tenant, from, until)
	if err != nil {
		t.Fatalf("SummarizePaid: %v", err)
	}
	if len(totals) != len(sums) {
		t.Fatalf("csv currencies=%v, summary=%+v, want the same buckets", sums, totals)
	}
	for _, bucket := range totals {
		if sums[bucket.Currency] != bucket.NetAmount || counts[bucket.Currency] != bucket.TransactionCount {
			t.Fatalf("csv %s={net:%d,count:%d}, summary=%+v", bucket.Currency, sums[bucket.Currency], counts[bucket.Currency], bucket)
		}
	}
}

// The list's created_at default must be untouched by the paid_at branch: a
// row created in range but paid outside it still lists under the default.
func TestListDateBasisKeepsCreatedAtDefaultPostgres(t *testing.T) {
	db := openSalesSummaryDatabase(t)
	repo := NewTransactionRepository(db).(*transactionRepository)
	tenant := uuid.New()
	from := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	createdInRange := from.AddDate(0, 0, 10)
	paidOutside := from.AddDate(1, 0, 0)

	tx := domain.Transaction{
		ID: uuid.New(), MerchantOrderID: "kel147-basis-" + uuid.NewString(), TenantID: tenant,
		ParentID: uuid.New(), StudentID: uuid.New(), EnrollmentID: uuid.New(),
		SubtotalAmount: 50000, GrossAmount: 50000, PlatformFee: 5000, NetAmount: 45000,
		Currency: "IDR", Status: domain.TransactionStatusPaid, BillingEmail: "parent@example.com",
		PaidAt: &paidOutside, CreatedAt: createdInRange,
	}
	if err := db.Create(&tx).Error; err != nil {
		t.Fatalf("seed transaction: %v", err)
	}
	t.Cleanup(func() { db.Unscoped().Delete(&domain.Transaction{}, "id = ?", tx.ID) })

	paged := domain.TransactionQuery{Page: 1, PageSize: 20, DateFrom: &from, DateTo: &createdInRange}
	items, _, err := repo.List(context.Background(), &tenant, nil, paged)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for i := range items {
		if items[i].ID == tx.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("default list missed a row created in range but paid outside it")
	}

	paged.DateBy = domain.TransactionDateByPaidAt
	items, _, err = repo.List(context.Background(), &tenant, nil, paged)
	if err != nil {
		t.Fatalf("List paid_at: %v", err)
	}
	for i := range items {
		if items[i].ID == tx.ID {
			t.Fatal("paid_at list returned a row paid outside the range")
		}
	}
}

// An empty paid range exports a header-only CSV.
func TestIterateExportEmptyRangePostgres(t *testing.T) {
	db := openSalesSummaryDatabase(t)
	repo := NewTransactionRepository(db).(*transactionRepository)
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC)
	query := domain.TransactionQuery{
		Status: domain.TransactionStatusPaid, DateBy: domain.TransactionDateByPaidAt,
		DateFrom: &from, DateTo: &to,
	}
	var streamed []domain.Transaction
	if err := repo.IterateExport(context.Background(), uuid.New(), query, 100, func(batch []domain.Transaction) error {
		streamed = append(streamed, batch...)
		return nil
	}); err != nil {
		t.Fatalf("IterateExport: %v", err)
	}
	if len(streamed) != 0 {
		t.Fatalf("streamed=%d rows, want none", len(streamed))
	}
}
