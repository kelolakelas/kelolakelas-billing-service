package database

import (
	"strings"
	"testing"
)

func TestBuildPostgresDSNUsesSSLMode(t *testing.T) {
	for _, test := range []struct {
		name     string
		sslMode  string
		expected string
	}{
		{name: "local development", sslMode: "disable", expected: "sslmode=disable"},
		{name: "neon", sslMode: "require", expected: "sslmode=require"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn := buildPostgresDSN("host", "5432", "user", "password", "db", test.sslMode)
			if !strings.Contains(dsn, test.expected) {
				t.Fatalf("dsn=%q, expected %q", dsn, test.expected)
			}
		})
	}
}
