package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// CI has no PostgreSQL, so this pins the statement shape the integration test proves:
// tenant, paid status, the half-open paid_at interval, soft-delete exclusion, per-currency grouping.
func TestSummarizePaidScopesTheAggregateStatement(t *testing.T) {
	tenantID := uuid.New()
	from := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	until := from.AddDate(0, 0, 30)
	repo, mock, cleanup := newTransactionMock(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT currency, COUNT(*) AS transaction_count, COALESCE(SUM(gross_amount), 0) AS gross_amount, COALESCE(SUM(net_amount), 0) AS net_amount FROM "transactions" WHERE (tenant_id = $1 AND status = $2 AND paid_at >= $3 AND paid_at < $4) AND "transactions"."deleted_at" IS NULL GROUP BY "currency" ORDER BY currency`,
	)).WithArgs(tenantID, domain.TransactionStatusPaid, from, until).
		WillReturnRows(sqlmock.NewRows([]string{"currency", "transaction_count", "gross_amount", "net_amount"}).
			AddRow("IDR", 2, 300000, 270000))

	totals, err := repo.SummarizePaid(context.Background(), tenantID, from, until)
	if err != nil {
		t.Fatalf("SummarizePaid: %v", err)
	}
	if want := (domain.SalesSummary{Currency: "IDR", TransactionCount: 2, GrossAmount: 300000, NetAmount: 270000}); len(totals) != 1 || totals[0] != want {
		t.Fatalf("totals=%+v, want [%+v]", totals, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
