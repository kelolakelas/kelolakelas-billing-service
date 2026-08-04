package config

import (
	"testing"

	"github.com/spf13/viper"
)

func TestLoadConfigDBSSLMode(t *testing.T) {
	for _, test := range []struct {
		name string
		env  string
		want string
	}{
		{name: "require", env: "require", want: "require"},
		{name: "local default", env: "", want: "disable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			viper.Reset()
			t.Setenv("DB_SSLMODE", test.env)
			t.Setenv("JWT_SECRET", "test-secret")
			config, err := LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if config.DBSSLMode != test.want {
				t.Fatalf("DBSSLMode=%q, want %q", config.DBSSLMode, test.want)
			}
		})
	}
}
