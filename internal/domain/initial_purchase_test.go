package domain

import (
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestInitialAndRenewalInvoiceIdentity(t *testing.T) {
	id, sub, period := uuid.New(), uuid.New(), time.Now()
	for _, tc := range []struct {
		name    string
		tx      Transaction
		renewal bool
	}{
		{"initial with period", Transaction{ID: id, EnrollmentID: id, SubscriptionID: &sub, BillingPeriodStart: &period}, false},
		{"renewal", Transaction{ID: uuid.New(), EnrollmentID: id, SubscriptionID: &sub, BillingPeriodStart: &period}, true},
		{"legacy without period", Transaction{ID: uuid.New(), EnrollmentID: id, SubscriptionID: &sub}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tx.IsRenewal(); got != tc.renewal {
				t.Fatalf("renewal=%v want %v", got, tc.renewal)
			}
		})
	}
}
