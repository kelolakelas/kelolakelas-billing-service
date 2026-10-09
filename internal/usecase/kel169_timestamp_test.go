package usecase

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

func TestKEL169InvoiceUTC(t *testing.T) {
	old := time.Local
	t.Cleanup(func() { time.Local = old })
	for _, zone := range []string{"Asia/Jakarta", "UTC"} {
		t.Run(zone, func(t *testing.T) {
			var err error
			time.Local, err = time.LoadLocation(zone)
			if err != nil {
				t.Fatal(err)
			}
			tx := claimTestTransaction(uuid.New())
			repo := &transactionRepoStub{transaction: tx}
			gateway := &invoiceGatewayStub{}
			cfg := claimTestConfig()
			u := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, cfg)
			before := time.Now().UTC()
			response, err := u.GenerateSubscriptionPayment(context.Background(), claimTestRequest(tx))
			if err != nil {
				t.Fatal(err)
			}
			after := time.Now().UTC()
			expiry := tx.InvoiceExpiresAt
			if expiry == nil || expiry.Location() != time.UTC || tx.UpdatedAt.Location() != time.UTC {
				t.Fatalf("timestamps not UTC: %+v", tx)
			}
			ttl := time.Duration(gateway.requests[0].ExpiryPeriod) * time.Minute
			created := expiry.Add(-ttl)
			if created.Before(before) || created.After(after) {
				t.Fatalf("expiry shifted: %v (creation window %v..%v)", expiry, before, after)
			}
			if response.TransactionID != tx.ID {
				t.Fatal("response transaction mismatch")
			}
			data, err := json.Marshal(transactionResponse(tx))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "+07:00") || !strings.Contains(string(data), "Z\"") {
				t.Fatalf("non-UTC response: %s", data)
			}
			for _, validity := range []int{0, -1, 60} {
				local := time.Now()
				got := domain.InvoiceExpiresAt(local, validity)
				want := validity
				if want <= 0 {
					want = domain.DefaultInvoiceValidityMinutes
				}
				if got.Location() != time.UTC || got.Sub(local) != time.Duration(want)*time.Minute {
					t.Fatalf("validity %d: %v", validity, got)
				}
			}
		})
	}
}
