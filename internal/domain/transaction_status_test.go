package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The transaction list filter is worth pinning to the code itself: `creating` was missing
// from a hand-written list before this change, which hid exactly the transactions the
// recovery path is meant to expose. This test reads the status constants out of the source
// and requires every one of them to be accepted as a filter, so adding a status the code
// writes without making it listable fails here instead of in production.
func TestTransactionStatusFilterValuesCoversEveryWrittenStatus(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "transaction.go", nil, 0)
	if err != nil {
		t.Fatalf("parse transaction.go: %v", err)
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
				if len(name.Name) < len("TransactionStatus") || name.Name[:len("TransactionStatus")] != "TransactionStatus" {
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
	if len(written) < 7 {
		t.Fatalf("found %d TransactionStatus constants, want at least the 7 the code writes", len(written))
	}

	filterable := map[string]bool{}
	for _, status := range TransactionStatusFilterValues() {
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

// An unknown filter has to be refused, because accepting it would return an empty page that
// reads as "no transactions" instead of "you asked for something that does not exist".
func TestIsTransactionStatusFilterValueRejectsUnknownValues(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{status: TransactionStatusCreating, want: true},
		{status: TransactionStatusPending, want: true},
		{status: "", want: false},
		{status: "Creating", want: false},
		{status: "creating ", want: false},
		{status: "not-a-status", want: false},
	}
	for _, test := range tests {
		if got := IsTransactionStatusFilterValue(test.status); got != test.want {
			t.Errorf("IsTransactionStatusFilterValue(%q) = %v, want %v", test.status, got, test.want)
		}
	}
}

// unquote strips the double quotes the literal carries in the source, which is enough here
// because the status constants are plain ASCII identifiers used as filter values.
func unquote(literal string) string {
	if len(literal) >= 2 && literal[0] == '"' && literal[len(literal)-1] == '"' {
		return literal[1 : len(literal)-1]
	}
	return literal
}
