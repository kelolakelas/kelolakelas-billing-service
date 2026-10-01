package domain

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNeutralizeCSVCellPrefixesFormulaTriggers(t *testing.T) {
	for _, value := range []string{"=cmd|'/c calc'!A0", "+SUM(A1:A2)", "-2+3", "@SUM(A1)"} {
		got := NeutralizeCSVCell(value)
		if !strings.HasPrefix(got, "'") || got[1:] != value {
			t.Fatalf("NeutralizeCSVCell(%q)=%q, want a single-quote prefix preserving the value", value, got)
		}
		if strings.HasPrefix(got[1:], "'") {
			t.Fatalf("NeutralizeCSVCell(%q)=%q, want exactly one quote", value, got)
		}
	}
}

func TestNeutralizeCSVCellLeavesOrdinaryValuesAlone(t *testing.T) {
	for _, value := range []string{"", "ORD-123", "paid", "IDR", "100000", "2026-09-01T00:00:00Z", " debt with leading space"} {
		if got := NeutralizeCSVCell(value); got != value {
			t.Fatalf("NeutralizeCSVCell(%q)=%q, want unchanged", value, got)
		}
	}
}

func exportTestTransaction() Transaction {
	paidAt := time.Date(2026, time.September, 14, 10, 30, 0, 0, time.FixedZone("WIB", 7*3600))
	return Transaction{
		MerchantOrderID:   "=HYPERLINK(\"http://evil.example\")",
		Status:            TransactionStatusPaid,
		StudentID:         uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		EnrollmentID:      uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		Currency:          "IDR",
		SubtotalAmount:    250000,
		DiscountAmount:    25000,
		GrossAmount:       225000,
		PlatformFee:       20000,
		PaymentGatewayFee: 5000,
		NetAmount:         200000,
		PaidAt:            &paidAt,
		CreatedAt:         time.Date(2026, time.September, 13, 17, 0, 0, 0, time.UTC),
	}
}

func TestFormatTransactionExportRowNeutralizesAndFormatsUTC(t *testing.T) {
	row := FormatTransactionExportRow(exportTestTransaction())
	if len(row) != len(TransactionExportColumns) {
		t.Fatalf("row has %d cells, want %d matching the header", len(row), len(TransactionExportColumns))
	}
	byName := map[string]string{}
	for i, name := range TransactionExportColumns {
		byName[name] = row[i]
	}
	if byName["order_id"] != "'=HYPERLINK(\"http://evil.example\")" {
		t.Fatalf("order_id=%q, want the injected value neutralized with a leading quote", byName["order_id"])
	}
	if byName["created_at"] != "2026-09-13T17:00:00Z" {
		t.Fatalf("created_at=%q, want UTC RFC3339", byName["created_at"])
	}
	// 10:30 WIB is 03:30 UTC: the export always renders UTC like the summary days.
	if byName["paid_at"] != "2026-09-14T03:30:00Z" {
		t.Fatalf("paid_at=%q, want UTC RFC3339", byName["paid_at"])
	}
	for name, want := range map[string]string{
		"status": "paid", "student_id": "11111111-1111-1111-1111-111111111111",
		"enrollment_id": "22222222-2222-2222-2222-222222222222", "currency": "IDR",
		"subtotal": "250000", "discount": "25000", "gross": "225000",
		"platform_fee": "20000", "gateway_fee": "5000", "net": "200000",
	} {
		if byName[name] != want {
			t.Fatalf("%s=%q, want %q", name, byName[name], want)
		}
	}
}

func TestFormatTransactionExportRowLeavesUnpaidPaidAtEmpty(t *testing.T) {
	tx := exportTestTransaction()
	tx.PaidAt = nil
	tx.Status = TransactionStatusPending
	row := FormatTransactionExportRow(tx)
	byName := map[string]string{}
	for i, name := range TransactionExportColumns {
		byName[name] = row[i]
	}
	if byName["paid_at"] != "" {
		t.Fatalf("paid_at=%q, want empty for an unpaid transaction", byName["paid_at"])
	}
}

func TestTransactionExportCSVRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewTransactionExportWriter(&buf)
	if err := WriteTransactionExportHeader(w); err != nil {
		t.Fatal(err)
	}
	tx := exportTestTransaction()
	tx.MerchantOrderID = "ORD, with \"comma\" and\nnewline"
	if err := WriteTransactionExportRow(w, tx); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("export is not valid CSV: %v\n%s", err, buf.String())
	}
	if len(records) != 2 || len(records[0]) != len(TransactionExportColumns) {
		t.Fatalf("records=%v, want header + one row with %d columns", records, len(TransactionExportColumns))
	}
	for i, name := range TransactionExportColumns {
		if records[0][i] != name {
			t.Fatalf("header[%d]=%q, want %q", i, records[0][i], name)
		}
	}
	if records[1][0] != "ORD, with \"comma\" and\nnewline" {
		t.Fatalf("order_id round-trip=%q, want the raw value preserved by quoting", records[1][0])
	}
}
