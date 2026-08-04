package database

import (
	"strings"
	"testing"
)

func TestBuildPostgresDSN(t *testing.T) {
	for _, test := range []struct {
		name     string
		sslMode  string
		binding  string
		expected string
	}{
		{name: "local development", sslMode: "disable", binding: "disable", expected: "sslmode=disable channel_binding=disable"},
		{name: "neon", sslMode: "require", binding: "require", expected: "sslmode=require channel_binding=require"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn := buildPostgresDSN("host", "5432", "user", "password", "db", test.sslMode, test.binding)
			if !strings.Contains(dsn, test.expected) {
				t.Fatalf("dsn=%q, expected %q", dsn, test.expected)
			}
		})
	}
}
