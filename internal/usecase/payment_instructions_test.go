package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type instructionGateway struct{ invoice domain.CreateInvoiceResponse }

func (g *instructionGateway) CreateInvoice(context.Context, *domain.CreateInvoiceRequest) (*domain.CreateInvoiceResponse, error) {
	return &g.invoice, nil
}
func (*instructionGateway) TransactionStatus(context.Context, string) (*domain.PaymentStatus, error) {
	return nil, nil
}
func (*instructionGateway) ValidateCallbackSignature(*domain.DuitkuCallbackPayload) bool { return true }

func TestPaymentInstructionsPersistAndReplaceWithoutStaleValues(t *testing.T) {
	old := "OLD-VA"
	past := time.Now().Add(-time.Hour)
	tx := &domain.Transaction{ID: uuid.New(), MerchantOrderID: uuid.NewString(), TenantID: uuid.New(), ParentID: uuid.New(), StudentID: uuid.New(), EnrollmentID: uuid.New(), GrossAmount: 40000, NetAmount: 40000, Currency: "IDR", Status: domain.TransactionStatusExpired, CheckoutSessionURL: strPtr("https://old.test"), VANumber: &old, QRString: &old, AppURL: &old, InvoiceExpiresAt: &past}
	repo := &transactionRepoStub{transaction: tx}
	gateway := &instructionGateway{invoice: domain.CreateInvoiceResponse{Reference: "NEW", PaymentURL: "https://new.test", QRString: "QR-PAYLOAD"}}
	u := newTransactionUsecaseForTest(repo, &reconciliationRepoStub{}, gateway, &academicActivationStub{}, expiryTestConfig())
	request := &domain.GenerateSubscriptionPaymentRequest{EnrollmentID: tx.EnrollmentID, TenantID: tx.TenantID, ParentID: tx.ParentID, BillingCycle: domain.BillingCycleMonthly, SubtotalAmount: 40000}
	if _, err := u.GenerateSubscriptionPayment(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	response, err := u.GetByIDScoped(context.Background(), nil, &tx.ParentID, tx.ID)
	if err != nil || response.QRString != "QR-PAYLOAD" || response.VANumber != "" || response.AppURL != "" || response.CheckoutSessionURL != "https://new.test" || tx.VANumber != nil || tx.AppURL != nil {
		t.Fatalf("replacement: %+v, stored: %+v, err: %v", response, tx, err)
	}
	if _, err := u.GenerateSubscriptionPayment(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if repo.claimCount != 1 {
		t.Fatalf("replay reissued invoice: %d claims", repo.claimCount)
	}
	// Hosted-only provider responses replace all prior direct instructions.
	tx.Status, tx.InvoiceExpiresAt = domain.TransactionStatusExpired, &past
	gateway.invoice = domain.CreateInvoiceResponse{Reference: "NEXT", PaymentURL: "https://hosted.test"}
	if _, err := u.GenerateSubscriptionPayment(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if tx.QRString != nil || transactionResponse(tx).QRString != "" {
		t.Fatal("QR retained after hosted-only reissue")
	}
}

func TestPaymentInstructionsOnlyOnActiveInvoiceAndOwner(t *testing.T) {
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	tx := &domain.Transaction{ID: uuid.New(), ParentID: uuid.New(), TenantID: uuid.New(), Status: domain.TransactionStatusPending, PaymentMethod: strPtr("VA"), CheckoutSessionURL: strPtr("https://hosted.test"), VANumber: strPtr("VA-PRIVATE"), QRString: strPtr("QR-PRIVATE"), AppURL: strPtr("https://app.test"), InvoiceExpiresAt: &future}
	u := &transactionUsecase{txRepo: &transactionRepoStub{transaction: tx}}
	result, err := u.GetByIDScoped(context.Background(), nil, &tx.ParentID, tx.ID)
	if err != nil || result.VANumber != "VA-PRIVATE" || result.PaymentMethod != "VA" {
		t.Fatalf("own parent: %+v %v", result, err)
	}
	for _, other := range []struct{ tenant, parent *uuid.UUID }{{parent: uuidPointer(uuid.New())}, {tenant: uuidPointer(uuid.New())}} {
		if _, err := u.GetByIDScoped(context.Background(), other.tenant, other.parent, tx.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("scope error: %v", err)
		}
	}
	if _, err := (&transactionUsecase{txRepo: &transactionRepoStub{missing: true}}).GetByIDScoped(context.Background(), nil, &tx.ParentID, tx.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing transaction: %v", err)
	}
	for _, status := range []string{domain.TransactionStatusExpired, domain.TransactionStatusFailed, domain.TransactionStatusCreating, domain.TransactionStatusCancelled, domain.TransactionStatusPaid, domain.TransactionStatusPending} {
		tx.Status, tx.InvoiceExpiresAt = status, &future
		if status == domain.TransactionStatusPending {
			tx.InvoiceExpiresAt = &past
		}
		response := transactionResponse(tx)
		encoded, _ := json.Marshal(response)
		if response.CheckoutSessionURL != "" || response.VANumber != "" || response.QRString != "" || response.AppURL != "" || strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), "hosted.test") {
			t.Fatalf("stale instructions exposed for %s: %s", status, encoded)
		}
	}
	// Legacy rows without the new nullable columns still expose hosted checkout.
	tx.Status, tx.InvoiceExpiresAt, tx.VANumber, tx.QRString, tx.AppURL = domain.TransactionStatusPending, &future, nil, nil, nil
	if response := transactionResponse(tx); response.CheckoutSessionURL == "" || response.VANumber != "" {
		t.Fatalf("legacy response: %+v", response)
	}
}

func uuidPointer(id uuid.UUID) *uuid.UUID { return &id }
