package domain

import (
	"encoding/csv"
	"io"
	"strconv"
	"time"
)

// TransactionExportColumns is the exact CSV column order for the tenant
// transaction export (KEL-147). Currency is included so multi-currency
// ranges stay reconcilable per currency instead of being summed.
var TransactionExportColumns = []string{
	"order_id",
	"created_at",
	"paid_at",
	"status",
	"student_id",
	"enrollment_id",
	"currency",
	"subtotal",
	"discount",
	"gross",
	"platform_fee",
	"gateway_fee",
	"net",
}

// NeutralizeCSVCell prefixes values that a spreadsheet could execute as a
// formula. Any cell starting with `=`, `+`, `-`, or `@` gets a leading single
// quote so Excel/Sheets/LibreOffice render it as text. encoding/csv still
// owns comma/quote/newline quoting; this only covers formula injection.
func NeutralizeCSVCell(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}

func formatExportTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

// FormatTransactionExportRow serializes one transaction into a CSV record
// following TransactionExportColumns. Amounts are plain base-unit integers
// (never negative in practice, but NeutralizeCSVCell still guards the sign).
// Times are UTC RFC3339; a missing paid_at serializes as an empty cell.
func FormatTransactionExportRow(tx Transaction) []string {
	paidAt := ""
	if tx.PaidAt != nil {
		paidAt = formatExportTime(*tx.PaidAt)
	}
	cells := []string{
		tx.MerchantOrderID,
		formatExportTime(tx.CreatedAt),
		paidAt,
		tx.Status,
		tx.StudentID.String(),
		tx.EnrollmentID.String(),
		tx.Currency,
		strconv.FormatInt(tx.SubtotalAmount, 10),
		strconv.FormatInt(tx.DiscountAmount, 10),
		strconv.FormatInt(tx.GrossAmount, 10),
		strconv.FormatInt(tx.PlatformFee, 10),
		strconv.FormatInt(tx.PaymentGatewayFee, 10),
		strconv.FormatInt(tx.NetAmount, 10),
	}
	for i := range cells {
		cells[i] = NeutralizeCSVCell(cells[i])
	}
	return cells
}

// WriteTransactionExportHeader writes the header row. It never emits a BOM:
// every column is ASCII-safe (UUIDs, ISO dates, status enums, integers), so
// plain RFC4180 output round-trips through strict parsers.
func WriteTransactionExportHeader(w *csv.Writer) error {
	return w.Write(TransactionExportColumns)
}

// WriteTransactionExportRow writes one data row.
func WriteTransactionExportRow(w *csv.Writer, tx Transaction) error {
	return w.Write(FormatTransactionExportRow(tx))
}

// NewTransactionExportWriter wraps w with encoding/csv using the default
// comma separator and CRLF-free LF line endings inherited from the package.
func NewTransactionExportWriter(w io.Writer) *csv.Writer {
	return csv.NewWriter(w)
}
