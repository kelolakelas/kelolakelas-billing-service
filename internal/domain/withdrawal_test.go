package domain

import "testing"

// KEL-143: on withdrawal-aware data, the signed ledger sum matches the
// available balance (payment credits, hold debits, cancel releases) and the
// held balance matches the still-open request amount. The total balance is
// conserved across the hold/cancel state transitions.
func TestWalletLedgerInvariantForWithdrawalHoldAndRelease(t *testing.T) {
	payment := int64(120000)
	hold := int64(60000)
	ledger := []int64{payment}
	wallet := &Wallet{AvailableBalance: payment, PendingBalance: 0}
	ledgerSum := func() int64 {
		var total int64
		for _, amount := range ledger {
			total += amount
		}
		return total
	}
	if !WalletLedgerInvariant(wallet, ledgerSum(), 0) {
		t.Fatal("payment-only state must satisfy the ledger invariant")
	}

	wallet.AvailableBalance -= hold
	wallet.PendingBalance += hold
	ledger = append(ledger, -hold)
	if !WalletLedgerInvariant(wallet, ledgerSum(), hold) || wallet.AvailableBalance+wallet.PendingBalance != payment {
		t.Fatalf("hold state: wallet=%+v ledger=%d, want total %d conserved", wallet, ledgerSum(), payment)
	}

	wallet.AvailableBalance += hold
	wallet.PendingBalance -= hold
	ledger = append(ledger, hold)
	if !WalletLedgerInvariant(wallet, ledgerSum(), 0) || wallet.AvailableBalance+wallet.PendingBalance != payment {
		t.Fatalf("cancel state: wallet=%+v ledger=%d, want total %d conserved", wallet, ledgerSum(), payment)
	}
	if WalletLedgerInvariant(wallet, ledgerSum()-1, 0) || WalletLedgerInvariant(wallet, ledgerSum(), 1) {
		t.Fatal("invariant must reject a missing ledger entry or an unexplained held balance")
	}
}
