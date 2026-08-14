package migration

import (
	"net/url"
	"testing"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
)

func TestAddChannelBinding(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantValue string
		wantSet   bool
	}{
		{name: "empty", value: "", wantSet: false},
		{name: "disable", value: "disable", wantSet: false},
		{name: "prefer", value: "prefer", wantValue: "prefer", wantSet: true},
		{name: "require", value: "require", wantValue: "require", wantSet: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := url.Values{"channel_binding": []string{"disable"}}
			addChannelBinding(query, test.value)
			value, ok := query["channel_binding"]
			if ok != test.wantSet {
				t.Fatalf("channel_binding present=%t, want %t", ok, test.wantSet)
			}
			if test.wantSet && (len(value) != 1 || value[0] != test.wantValue) {
				t.Fatalf("channel_binding=%v, want %q", value, test.wantValue)
			}
		})
	}
}

func TestBuildDatabaseURLWithDatabaseURL(t *testing.T) {
	tests := []struct {
		name           string
		databaseURL    string
		channelBinding string
		wantBinding    string
	}{
		{name: "without channel binding", databaseURL: "postgres://user:password@localhost:5432/billing?sslmode=require"},
		{name: "with valid channel binding", databaseURL: "postgres://user:password@localhost:5432/billing?sslmode=require&channel_binding=require", channelBinding: "require", wantBinding: "require"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := buildDatabaseURL(config.Config{DatabaseURL: test.databaseURL, DBChannelBinding: test.channelBinding})
			if err != nil {
				t.Fatalf("buildDatabaseURL() error = %v", err)
			}
			parsed, err := url.Parse(value)
			if err != nil {
				t.Fatalf("url.Parse() error = %v", err)
			}
			if got := parsed.Query().Get("channel_binding"); got != test.wantBinding {
				t.Fatalf("channel_binding=%q, want %q", got, test.wantBinding)
			}
			if parsed.Query().Get("channel_binding") == "disable" {
				t.Fatal("migration URL contains channel_binding=disable")
			}
		})
	}
}
