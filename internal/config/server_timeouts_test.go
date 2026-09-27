package config

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/kelolakelas/kelolakelas-billing-service/pkg/academic"
)

func loadWithEnv(t *testing.T, env map[string]string) (Config, error) {
	t.Helper()
	viper.Reset()
	t.Chdir(t.TempDir())
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("INTERNAL_SERVICE_CREDENTIAL", "test-internal-credential")
	for _, key := range []string{
		"SERVER_READ_HEADER_TIMEOUT_SECONDS", "SERVER_READ_TIMEOUT_SECONDS", "SERVER_WRITE_TIMEOUT_SECONDS", "SERVER_IDLE_TIMEOUT_SECONDS",
		"DUITKU_HTTP_TIMEOUT_SECONDS", "RESEND_HTTP_TIMEOUT_SECONDS", "IDENTITY_PERMISSION_TIMEOUT_MS",
	} {
		t.Setenv(key, "")
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	return LoadConfig()
}

// KEL-71: the billing server timeouts have safe defaults, can be overridden, and a zero
// or negative value falls back to the default instead of disabling the bound.
func TestLoadConfigServerTimeouts(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		env                       map[string]string
		header, read, write, idle int
	}{
		{
			name:   "defaults",
			header: DefaultServerReadHeaderTimeout, read: DefaultServerReadTimeout,
			write: DefaultServerWriteTimeout, idle: DefaultServerIdleTimeout,
		},
		{
			name: "overrides",
			env: map[string]string{
				"SERVER_READ_HEADER_TIMEOUT_SECONDS": "2", "SERVER_READ_TIMEOUT_SECONDS": "15",
				"SERVER_WRITE_TIMEOUT_SECONDS": "45", "SERVER_IDLE_TIMEOUT_SECONDS": "90",
			},
			header: 2, read: 15, write: 45, idle: 90,
		},
		{
			name: "zero uses defaults",
			env: map[string]string{
				"SERVER_READ_HEADER_TIMEOUT_SECONDS": "0", "SERVER_READ_TIMEOUT_SECONDS": "0",
				"SERVER_WRITE_TIMEOUT_SECONDS": "0", "SERVER_IDLE_TIMEOUT_SECONDS": "0",
			},
			header: DefaultServerReadHeaderTimeout, read: DefaultServerReadTimeout,
			write: DefaultServerWriteTimeout, idle: DefaultServerIdleTimeout,
		},
		{
			name: "negative uses defaults",
			env: map[string]string{
				"SERVER_READ_HEADER_TIMEOUT_SECONDS": "-1", "SERVER_READ_TIMEOUT_SECONDS": "-2",
				"SERVER_WRITE_TIMEOUT_SECONDS": "-3", "SERVER_IDLE_TIMEOUT_SECONDS": "-4",
			},
			header: DefaultServerReadHeaderTimeout, read: DefaultServerReadTimeout,
			write: DefaultServerWriteTimeout, idle: DefaultServerIdleTimeout,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadWithEnv(t, tc.env)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ServerReadHeaderTimeout != tc.header || cfg.ServerReadTimeout != tc.read ||
				cfg.ServerWriteTimeout != tc.write || cfg.ServerIdleTimeout != tc.idle {
				t.Fatalf("timeouts = %d/%d/%d/%d, want %d/%d/%d/%d",
					cfg.ServerReadHeaderTimeout, cfg.ServerReadTimeout, cfg.ServerWriteTimeout, cfg.ServerIdleTimeout,
					tc.header, tc.read, tc.write, tc.idle)
			}
		})
	}
}

// KEL-71: the default write timeout exceeds the webhook's worst-case outbound chain
// (Duitku status check + Resend email + Academic activation) and the permission check.
func TestDefaultWriteTimeoutExceedsLongestOutboundWait(t *testing.T) {
	cfg, err := loadWithEnv(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	name, longest := cfg.LongestRequestOutboundTimeout()
	want := time.Duration(DefaultProviderHTTPTimeoutSeconds)*2*time.Second + academic.RequestTimeout
	if longest != want {
		t.Fatalf("longest outbound wait = %s (%s), want %s", longest, name, want)
	}
	if time.Duration(cfg.ServerWriteTimeout)*time.Second <= longest {
		t.Fatalf("write timeout %ds does not exceed %s", cfg.ServerWriteTimeout, longest)
	}
}

// KEL-71: a write timeout at or below the longest outbound wait, or a value too large
// for a duration, stops startup with an error that names the setting.
func TestLoadConfigRejectsUnsafeServerTimeouts(t *testing.T) {
	tooLarge := strconv.Itoa(maxDurationSeconds + 1)
	for _, tc := range []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "write equals default outbound chain",
			env:     map[string]string{"SERVER_WRITE_TIMEOUT_SECONDS": "30"},
			wantErr: "SERVER_WRITE_TIMEOUT_SECONDS (30) must be greater",
		},
		{
			name:    "write below Duitku timeout",
			env:     map[string]string{"SERVER_WRITE_TIMEOUT_SECONDS": "40", "DUITKU_HTTP_TIMEOUT_SECONDS": "45"},
			wantErr: "Duitku webhook",
		},
		{
			name:    "write below identity permission timeout",
			env:     map[string]string{"SERVER_WRITE_TIMEOUT_SECONDS": "40", "IDENTITY_PERMISSION_TIMEOUT_MS": "50000"},
			wantErr: "IDENTITY_PERMISSION_TIMEOUT_MS",
		},
		{
			name:    "huge provider timeout saturates instead of overflowing",
			env:     map[string]string{"DUITKU_HTTP_TIMEOUT_SECONDS": tooLarge},
			wantErr: "SERVER_WRITE_TIMEOUT_SECONDS",
		},
		{
			name:    "read header too large",
			env:     map[string]string{"SERVER_READ_HEADER_TIMEOUT_SECONDS": tooLarge},
			wantErr: "SERVER_READ_HEADER_TIMEOUT_SECONDS",
		},
		{
			name:    "idle too large",
			env:     map[string]string{"SERVER_IDLE_TIMEOUT_SECONDS": tooLarge},
			wantErr: "SERVER_IDLE_TIMEOUT_SECONDS",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadWithEnv(t, tc.env)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// A write timeout just above a raised outbound chain is accepted.
func TestLoadConfigAcceptsWriteTimeoutAboveRaisedOutboundChain(t *testing.T) {
	cfg, err := loadWithEnv(t, map[string]string{
		"SERVER_WRITE_TIMEOUT_SECONDS": "36",
		"DUITKU_HTTP_TIMEOUT_SECONDS":  "20",
		"RESEND_HTTP_TIMEOUT_SECONDS":  "5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerWriteTimeout != 36 {
		t.Fatalf("write timeout = %d, want 36", cfg.ServerWriteTimeout)
	}
}
