package migration

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

func TestPaymentInstructionsMigrationPostgres(t *testing.T) {
	dsn, db := kel59Database(t)
	m := kel59Migrator(t, dsn)
	if err := m.Migrate(kel99SnapshotVersion); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if err := db.Exec(`INSERT INTO transactions (id, merchant_order_id, tenant_id, parent_id, student_id, enrollment_id, subtotal_amount, discount_amount, gross_amount, platform_fee, payment_gateway_fee, net_amount, currency, status, billing_email, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 40000, 0, 40000, 0, 0, 40000, 'IDR', 'pending', '', now(), now())`, id, uuid.NewString(), uuid.New(), uuid.New(), uuid.New(), uuid.New()).Error; err != nil {
		t.Fatal(err)
	}
	const version = 20260929000000
	if err := m.Migrate(version); err != nil {
		t.Fatal(err)
	}
	repo := repository.NewTransactionRepository(db)
	stored, err := repo.GetByID(context.Background(), id)
	if err != nil || stored.VANumber != nil || stored.QRString != nil || stored.AppURL != nil {
		t.Fatalf("legacy row after up: %+v %v", stored, err)
	}
	if err := db.Model(&domain.Transaction{}).Where("id = ?", id).Updates(map[string]interface{}{"va_number": "VA", "qr_string": "QR", "app_url": "APP"}).Error; err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetByID(context.Background(), id)
	if err != nil || stored.VANumber == nil || *stored.VANumber != "VA" || stored.QRString == nil || *stored.QRString != "QR" || stored.AppURL == nil || *stored.AppURL != "APP" {
		t.Fatalf("instructions after up: %+v %v", stored, err)
	}
	if err := m.Steps(-1); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Raw(`SELECT count(*) FROM transactions WHERE id = ? AND status = 'pending'`, id).Scan(&count).Error; err != nil || count != 1 {
		t.Fatalf("legacy data after down: %d %v", count, err)
	}
	for _, column := range []string{"va_number", "qr_string", "app_url"} {
		if db.Migrator().HasColumn("transactions", column) {
			t.Fatalf("column %s survived down", column)
		}
	}
	if err := m.Migrate(version); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetByID(context.Background(), id)
	if err != nil || stored.VANumber != nil || stored.QRString != nil || stored.AppURL != nil {
		t.Fatalf("legacy row after re-up: %+v %v", stored, err)
	}
}
