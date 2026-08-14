package config

import (
	"testing"

	"github.com/spf13/viper"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*testing.T)
		wantHost    string
		wantPort    string
		wantDBName  string
		wantBinding string
	}{
		{
			name: "environment variables are loaded",
			setup: func(t *testing.T) {
				t.Setenv("JWT_SECRET", "railway-jwt-secret")
				t.Setenv("DB_HOST", "railway-db.internal")
				t.Setenv("PORT", "19082")
			},
			wantHost: "railway-db.internal", wantPort: "19082", wantDBName: "kelolakelas_billing", wantBinding: "disable",
		},
		{
			name: "environment overrides defaults",
			setup: func(t *testing.T) {
				t.Setenv("JWT_SECRET", "test-secret")
				t.Setenv("PORT", "49154")
			},
			wantHost: "localhost", wantPort: "49154", wantDBName: "kelolakelas_billing", wantBinding: "disable",
		},
		{
			name:     "missing optional variables use defaults",
			setup:    func(t *testing.T) { t.Setenv("JWT_SECRET", "test-secret") },
			wantHost: "localhost", wantPort: "8082", wantDBName: "kelolakelas_billing", wantBinding: "disable",
		},
		{
			name: "DATABASE_URL supplies database settings",
			setup: func(t *testing.T) {
				t.Setenv("JWT_SECRET", "test-secret")
				t.Setenv("DATABASE_URL", "postgres://billing_user:billing_password@postgres.internal:6543/billing_db?sslmode=require&channel_binding=require")
			},
			wantHost: "postgres.internal", wantPort: "8082", wantDBName: "billing_db", wantBinding: "require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			t.Chdir(t.TempDir())
			for _, key := range []string{"DATABASE_URL", "DB_HOST", "DB_PORT", "DB_SSLMODE", "DB_CHANNEL_BINDING", "DB_USER", "DB_PASSWORD", "DB_NAME", "PORT", "JWT_SECRET", "INTERNAL_SERVICE_CREDENTIAL"} {
				t.Setenv(key, "")
			}
			t.Setenv("INTERNAL_SERVICE_CREDENTIAL", "test-internal-credential")
			if tt.setup != nil {
				tt.setup(t)
			}

			config, err := LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if config.DBHost != tt.wantHost || config.Port != tt.wantPort || config.DBName != tt.wantDBName || config.DBChannelBinding != tt.wantBinding {
				t.Fatalf("config database=%s port=%s name=%s, want database=%s port=%s name=%s", config.DBHost, config.Port, config.DBName, tt.wantHost, tt.wantPort, tt.wantDBName)
			}
		})
	}
}

func TestChannelBindingEnvironmentOverridesDatabaseURL(t *testing.T) {
	viper.Reset()
	t.Chdir(t.TempDir())
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("INTERNAL_SERVICE_CREDENTIAL", "test-internal-credential")
	t.Setenv("DATABASE_URL", "postgres://user:password@localhost/db?channel_binding=require")
	t.Setenv("DB_CHANNEL_BINDING", "disable")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.DBChannelBinding != "disable" {
		t.Fatalf("channel binding=%q, want disable", config.DBChannelBinding)
	}
}

func TestLoadConfigRejectsInvalidChannelBinding(t *testing.T) {
	viper.Reset()
	t.Chdir(t.TempDir())
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("INTERNAL_SERVICE_CREDENTIAL", "test-internal-credential")
	t.Setenv("DB_CHANNEL_BINDING", "invalid")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected invalid channel binding configuration error")
	}
}

func TestLoadConfigReadsDisabledChannelBindingFromDatabaseURL(t *testing.T) {
	viper.Reset()
	t.Chdir(t.TempDir())
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("INTERNAL_SERVICE_CREDENTIAL", "test-internal-credential")
	t.Setenv("DATABASE_URL", "postgres://user:password@localhost/db?sslmode=disable&channel_binding=disable")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.DBChannelBinding != "disable" {
		t.Fatalf("channel binding=%q, want disable", config.DBChannelBinding)
	}
}

func TestLoadConfigRequiresJWTSecret(t *testing.T) {
	viper.Reset()
	t.Chdir(t.TempDir())
	t.Setenv("JWT_SECRET", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected missing JWT_SECRET error")
	}
}
