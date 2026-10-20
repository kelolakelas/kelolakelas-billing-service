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

// Run the inclusive-end regression in CI even when PostgreSQL is unavailable.
func TestCreatedDateRangeIncludesFinalDay(t *testing.T) {
	for _, basis := range []string{"", domain.TransactionDateByCreatedAt} {
		t.Run(basis, func(t *testing.T) {
			repo, mock, cleanup := newTransactionMock(t)
			defer cleanup()
			tenant := uuid.New()
			day := time.Date(2026, time.October, 20, 0, 0, 0, 0, time.UTC)
			until := day.AddDate(0, 0, 1)
			query := domain.TransactionQuery{Page: 1, PageSize: 20, Status: "pending", DateBy: basis, DateFrom: &day, DateTo: &day}
			predicate := ` FROM "transactions" WHERE tenant_id = $1 AND status = $2 AND created_at >= $3 AND created_at < $4 AND "transactions"."deleted_at" IS NULL`
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*)`+predicate)).WithArgs(tenant, "pending", day, until).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT *`+predicate+` ORDER BY created_at DESC LIMIT $5`)).WithArgs(tenant, "pending", day, until, 20).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			if _, _, err := repo.List(context.Background(), &tenant, nil, query); err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT *`+predicate+` ORDER BY created_at ASC, id ASC LIMIT $5`)).WithArgs(tenant, "pending", day, until, 2).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			if err := repo.IterateExport(context.Background(), tenant, query, 2, func([]domain.Transaction) error {
				t.Fatal("empty export unexpectedly emitted a batch")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
