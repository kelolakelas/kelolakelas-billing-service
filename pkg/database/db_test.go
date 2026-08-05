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
		contains string
		absent   string
	}{
		{name: "local development", sslMode: "disable", binding: "disable", contains: "sslmode=disable", absent: "channel_binding="},
		{name: "empty binding", sslMode: "disable", binding: "", contains: "sslmode=disable", absent: "channel_binding="},
		{name: "neon", sslMode: "require", binding: "require", contains: "sslmode=require channel_binding=require"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn := buildPostgresDSN("host", "5432", "user", "password", "db", test.sslMode, test.binding)
			if !strings.Contains(dsn, test.contains) {
				t.Fatalf("dsn=%q, expected to contain %q", dsn, test.contains)
			}
			if test.absent != "" && strings.Contains(dsn, test.absent) {
				t.Fatalf("dsn=%q, expected not to contain %q", dsn, test.absent)
			}
		})
	}
}
