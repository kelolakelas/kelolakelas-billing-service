package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The accepted reconciliation status filter is worth pinning to the code itself for the
// same reason the transaction status filter is: the internal list endpoint exists so an
// operator can find a row by the status they observe, and a status the state machine can
// write but the endpoint refuses would hide exactly the failures the endpoint was added
// for. This test reads the status constants out of the source and requires each of them to
// be accepted, so adding a status without making it listable fails here instead of in
// production.
func TestReconciliationStatusValuesCoversEveryWrittenStatus(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "payment_reconciliation.go", nil, 0)
	if err != nil {
		t.Fatalf("parse payment_reconciliation.go: %v", err)
	}

	written := map[string]bool{}
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range values.Names {
				if len(name.Name) < len("ReconciliationStatus") || name.Name[:len("ReconciliationStatus")] != "ReconciliationStatus" {
					continue
				}
				if i >= len(values.Values) {
					continue
				}
				literal, ok := values.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				written[unquote(literal.Value)] = true
			}
		}
	}
	if len(written) != 4 {
		t.Fatalf("found %d ReconciliationStatus constants, want the 4 the state machine writes", len(written))
	}

	filterable := map[string]bool{}
	for _, status := range ReconciliationStatusValues() {
		filterable[status] = true
	}
	for status := range written {
		if !filterable[status] {
			t.Errorf("status %q is written by the code but is not an accepted list filter", status)
		}
	}
	for status := range filterable {
		if !written[status] {
			t.Errorf("filter %q is accepted but is not a status the code writes", status)
		}
	}
}

// An unknown status has to be refused, because accepting it would return an empty page
// that reads as "no failed reconciliations" instead of "you asked for a status that does
// not exist".
func TestIsReconciliationStatusValueRejectsUnknownValues(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{ReconciliationStatusPending, true},
		{ReconciliationStatusProcessing, true},
		{ReconciliationStatusActive, true},
		{ReconciliationStatusTerminalFailed, true},
		{"", false},
		{"failed", false},
		{"terminal-failed", false},
		{"Terminal_failed", false},
	}
	for _, test := range tests {
		if got := IsReconciliationStatusValue(test.status); got != test.want {
			t.Errorf("IsReconciliationStatusValue(%q) = %v, want %v", test.status, got, test.want)
		}
	}
}
