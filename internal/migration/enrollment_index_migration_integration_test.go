package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

const (
	kel59PreVersion   = 20260927000000
	kel59IndexVersion = 20260927120000
	kel59Index        = "idx_transactions_enrollment_created_at"
	kel59IndexDef     = "CREATE INDEX idx_transactions_enrollment_created_at ON public.transactions USING btree (enrollment_id, created_at)"
)

// kel59Database creates an empty, uniquely named database and returns its DSN.
// Set KEL59_TEST_ADMIN_DATABASE_URL to a PostgreSQL URL whose role may
// CREATE/DROP DATABASE; every run gets its own database so migration state
// never leaks between runs.
func kel59Database(t *testing.T) (string, *gorm.DB) {
	t.Helper()
	admin := os.Getenv("KEL59_TEST_ADMIN_DATABASE_URL")
	if admin == "" {
		t.Skip("set KEL59_TEST_ADMIN_DATABASE_URL for the enrollment index migration PostgreSQL test")
	}
	adminDB, err := gorm.Open(postgres.Open(admin), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	name := "kel59_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := adminDB.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	dsn := parsed.String()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = adminDB.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)").Error
		if sqlDB, err := adminDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return dsn, db
}

func kel59Migrator(t *testing.T, dsn string) *migrate.Migrate {
	t.Helper()
	dir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New((&url.URL{Scheme: "file", Path: dir}).String(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.Close() })
	if err := m.Migrate(kel59PreVersion); err != nil {
		t.Fatalf("migrate to %d: %v", kel59PreVersion, err)
	}
	return m
}

type kel59IndexState struct {
	IsUnique bool
	IsValid  bool
	Def      string
}

func kel59Lookup(t *testing.T, db *gorm.DB) *kel59IndexState {
	t.Helper()
	var rows []kel59IndexState
	err := db.Raw(`SELECT i.indisunique AS is_unique, i.indisvalid AS is_valid, pg_get_indexdef(i.indexrelid) AS def
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = ?`, kel59Index).Scan(&rows).Error
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// sqlCapture records every statement GORM executes, with bound values inlined,
// so the test EXPLAINs exactly the SQL the repository sends rather than a copy.
type sqlCapture struct {
	logger.Interface
	mu   sync.Mutex
	sqls []string
}

func (c *sqlCapture) LogMode(logger.LogLevel) logger.Interface { return c }

func (c *sqlCapture) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	c.mu.Lock()
	c.sqls = append(c.sqls, sql)
	c.mu.Unlock()
}

func (c *sqlCapture) take(t *testing.T, contains ...string) []string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, sql := range c.sqls {
		match := true
		for _, want := range contains {
			match = match && strings.Contains(sql, want)
		}
		if match {
			out = append(out, sql)
		}
	}
	c.sqls = nil
	if len(out) == 0 {
		t.Fatalf("no captured statement contains %q", contains)
	}
	return out
}

// kel59Plan runs EXPLAIN on sql and returns the text plan plus every index the
// plan reads.
func kel59Plan(t *testing.T, db *gorm.DB, sql string) (string, map[string]bool) {
	t.Helper()
	var lines []string
	if err := db.Raw("EXPLAIN " + sql).Scan(&lines).Error; err != nil {
		t.Fatalf("explain %s: %v", sql, err)
	}
	var raw string
	if err := db.Raw("EXPLAIN (FORMAT JSON) " + sql).Row().Scan(&raw); err != nil {
		t.Fatalf("explain json %s: %v", sql, err)
	}
	var plans []struct {
		Plan map[string]any `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(raw), &plans); err != nil || len(plans) != 1 {
		t.Fatalf("parse plan %q: %v", raw, err)
	}
	indexes := map[string]bool{}
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		if name, ok := node["Index Name"].(string); ok {
			indexes[name] = true
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			if m, ok := child.(map[string]any); ok {
				walk(m)
			}
		}
	}
	walk(plans[0].Plan)
	return strings.Join(lines, "\n"), indexes
}

// KEL-59: the migration adds a non-unique (enrollment_id, created_at) index, the
// per-enrollment lookups the repository issues use it, their results are
// unchanged, and down removes only that index.
func TestMigrationEnrollmentLookupIndexUpExplainDown(t *testing.T) {
	dsn, db := kel59Database(t)
	m := kel59Migrator(t, dsn)
	ctx := context.Background()

	// 12,000 transactions over 3,000 enrollments (4 per enrollment: first invoice,
	// renewals, retries) under one tenant, so neither the tenant index nor a
	// sequential scan is a cheaper way to find one enrollment's rows.
	tenant := uuid.New()
	if err := db.Exec(`INSERT INTO transactions (merchant_order_id, tenant_id, parent_id, student_id, enrollment_id,
			subtotal_amount, gross_amount, platform_fee, net_amount, status, billing_email, created_at)
		SELECT 'kel59-' || g, ?, gen_random_uuid(), gen_random_uuid(), md5('kel59-enrollment-' || (g % 3000))::uuid,
			100000, 100000, 10000, 90000, 'paid', 'parent@example.com', timestamp '2026-09-01' + g * interval '1 minute'
		FROM generate_series(1, 12000) AS g`, tenant).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	var enrollmentText string
	if err := db.Raw(`SELECT md5('kel59-enrollment-7')::uuid::text`).Row().Scan(&enrollmentText); err != nil {
		t.Fatal(err)
	}
	enrollment := uuid.MustParse(enrollmentText)

	capture := &sqlCapture{Interface: logger.Discard}
	repo := repository.NewTransactionRepository(db.Session(&gorm.Session{Logger: capture}))
	listQuery := domain.TransactionQuery{Page: 1, PageSize: 20, EnrollmentID: &enrollment}
	type lookup struct {
		latest   uuid.UUID
		listed   []uuid.UUID
		total    int64
		getSQL   string
		listSQLs []string
	}
	run := func() lookup {
		t.Helper()
		latest, err := repo.GetByEnrollmentID(ctx, enrollment)
		if err != nil {
			t.Fatalf("GetByEnrollmentID: %v", err)
		}
		getSQL := capture.take(t, `"transactions"`, "enrollment_id")[0]
		items, total, err := repo.List(ctx, &tenant, nil, listQuery)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		ids := make([]uuid.UUID, len(items))
		for i, item := range items {
			ids[i] = item.ID
		}
		return lookup{latest: latest.ID, listed: ids, total: total, getSQL: getSQL, listSQLs: capture.take(t, `"transactions"`, "enrollment_id")}
	}
	analyze := func() {
		t.Helper()
		if err := db.Exec("ANALYZE transactions").Error; err != nil {
			t.Fatal(err)
		}
	}

	analyze()
	before := run()
	if len(before.listSQLs) != 2 || before.total != 4 || len(before.listed) != 4 {
		t.Fatalf("baseline list: %d statements, total=%d, rows=%d", len(before.listSQLs), before.total, len(before.listed))
	}
	for _, sql := range append([]string{before.getSQL}, before.listSQLs...) {
		plan, _ := kel59Plan(t, db, sql)
		t.Logf("BEFORE %d\n%s\n%s", kel59IndexVersion, sql, plan)
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("up: %v", err)
	}
	if version, dirty, err := m.Version(); err != nil || version != kel59IndexVersion || dirty {
		t.Fatalf("version after up = %d dirty=%v err=%v", version, dirty, err)
	}
	state := kel59Lookup(t, db)
	if state == nil || state.IsUnique || !state.IsValid || state.Def != kel59IndexDef {
		t.Fatalf("index after up = %+v, want a valid non-unique %q", state, kel59IndexDef)
	}

	analyze()
	after := run()
	if after.latest != before.latest || after.total != before.total || fmt.Sprint(after.listed) != fmt.Sprint(before.listed) {
		t.Fatalf("results changed: before=%+v after=%+v", before, after)
	}
	if after.getSQL != before.getSQL || fmt.Sprint(after.listSQLs) != fmt.Sprint(before.listSQLs) {
		t.Fatalf("repository SQL changed:\n%s\n%s", before.getSQL, after.getSQL)
	}
	for _, sql := range append([]string{after.getSQL}, after.listSQLs...) {
		plan, indexes := kel59Plan(t, db, sql)
		t.Logf("AFTER %d\n%s\n%s", kel59IndexVersion, sql, plan)
		if !indexes[kel59Index] {
			t.Fatalf("plan does not use %s for %s:\n%s", kel59Index, sql, plan)
		}
	}

	// Several transactions per enrollment must stay insertable (non-unique).
	if err := db.Exec(`INSERT INTO transactions (merchant_order_id, tenant_id, parent_id, student_id, enrollment_id,
			subtotal_amount, gross_amount, platform_fee, net_amount, status, billing_email)
		VALUES ('kel59-extra', ?, gen_random_uuid(), gen_random_uuid(), ?, 1, 1, 0, 1, 'pending', 'parent@example.com')`,
		tenant, enrollment).Error; err != nil {
		t.Fatalf("second transaction for one enrollment rejected: %v", err)
	}

	if err := m.Steps(-1); err != nil {
		t.Fatalf("down: %v", err)
	}
	if version, dirty, err := m.Version(); err != nil || version != kel59PreVersion || dirty {
		t.Fatalf("version after down = %d dirty=%v err=%v", version, dirty, err)
	}
	if state := kel59Lookup(t, db); state != nil {
		t.Fatalf("%s still present after down: %+v", kel59Index, state)
	}
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM transactions`).Scan(&rows).Error; err != nil || rows != 12001 {
		t.Fatalf("down changed data: rows=%d err=%v", rows, err)
	}
	for _, other := range []string{"idx_transactions_tenant_status", "idx_transactions_due_expiry", "idx_transactions_stale_invoice_claim"} {
		var n int64
		if err := db.Raw(`SELECT count(*) FROM pg_indexes WHERE indexname = ?`, other).Scan(&n).Error; err != nil || n != 1 {
			t.Fatalf("down touched %s: count=%d err=%v", other, n, err)
		}
	}

	// Up tolerates an index that an operator already created by hand.
	if err := db.Exec(`CREATE INDEX ` + kel59Index + ` ON transactions (enrollment_id, created_at)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := m.Steps(1); err != nil {
		t.Fatalf("up over a pre-existing index: %v", err)
	}
	if state := kel59Lookup(t, db); state == nil || state.Def != kel59IndexDef {
		t.Fatalf("index after re-apply = %+v", state)
	}
}
