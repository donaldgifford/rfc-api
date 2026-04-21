package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
)

// Every test sets required fields to minimum valid values unless the
// test is specifically exercising Validate().
func withRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://x:y@localhost:5432/z")
	t.Setenv("MEILI_MASTER_KEY", "dev-key")
	t.Setenv("RFC_API_WEBHOOK_SECRET", "dev-secret")
}

func TestLoad_Defaults(t *testing.T) {
	withRequired(t)

	cfg, err := config.Load(nil, "")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	cases := []struct {
		name string
		got  any
		want any
	}{
		{"Server.Listen", cfg.Server.Listen, ":8080"},
		{"Admin.Listen", cfg.Admin.Listen, "127.0.0.1:8081"},
		{"Admin.PprofEnabled", cfg.Admin.PprofEnabled, false},
		{"Log.Level", cfg.Log.Level, "info"},
		{"Log.Format", cfg.Log.Format, "json"},
		{"RateLimit.RPS", cfg.RateLimit.RPS, 50},
		{"RateLimit.Burst", cfg.RateLimit.Burst, 100},
		{"RateLimit.TTL", cfg.RateLimit.TTL, time.Hour},
		{"OTel.TraceSampleRatio", cfg.OTel.TraceSampleRatio, 0.1},
		{"ShutdownTimeout", cfg.ShutdownTimeout, 20 * time.Second},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("default %s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoad_Env(t *testing.T) {
	withRequired(t)
	t.Setenv("RFC_API_LISTEN", ":9090")
	t.Setenv("RFC_API_ADMIN_LISTEN", "127.0.0.1:9091")
	t.Setenv("RFC_API_PPROF_ENABLED", "true")
	t.Setenv("RFC_API_LOG_LEVEL", "debug")
	t.Setenv("RFC_API_RATE_LIMIT_RPS", "200")
	t.Setenv("RFC_API_RATE_LIMIT_BURST", "400")
	t.Setenv("RFC_API_RATE_LIMIT_TTL", "30m")
	t.Setenv("RFC_API_CORS_ORIGINS", "https://a.example, https://b.example")
	t.Setenv("RFC_API_TRACE_SAMPLE_RATIO", "0.5")
	t.Setenv("RFC_API_SHUTDOWN_TIMEOUT", "45s")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")

	cfg, err := config.Load(nil, "")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Server.Listen", cfg.Server.Listen, ":9090"},
		{"Admin.Listen", cfg.Admin.Listen, "127.0.0.1:9091"},
		{"Admin.PprofEnabled", cfg.Admin.PprofEnabled, true},
		{"Log.Level", cfg.Log.Level, "debug"},
		{"RateLimit.RPS", cfg.RateLimit.RPS, 200},
		{"RateLimit.Burst", cfg.RateLimit.Burst, 400},
		{"RateLimit.TTL", cfg.RateLimit.TTL, 30 * time.Minute},
		{"OTel.TraceSampleRatio", cfg.OTel.TraceSampleRatio, 0.5},
		{"OTel.OTLPEndpoint", cfg.OTel.OTLPEndpoint, "http://localhost:4317"},
		{"ShutdownTimeout", cfg.ShutdownTimeout, 45 * time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("env %s = %v, want %v", c.name, c.got, c.want)
		}
	}

	wantCORS := []string{"https://a.example", "https://b.example"}
	if len(cfg.Server.CORSOrigins) != len(wantCORS) {
		t.Fatalf("CORSOrigins len = %d, want %d (got %v)",
			len(cfg.Server.CORSOrigins), len(wantCORS), cfg.Server.CORSOrigins)
	}
	for i, want := range wantCORS {
		if cfg.Server.CORSOrigins[i] != want {
			t.Errorf("CORSOrigins[%d] = %q, want %q",
				i, cfg.Server.CORSOrigins[i], want)
		}
	}
}

func TestLoad_File(t *testing.T) {
	withRequired(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `server:
  listen: ":7777"
  read_timeout: 22s
admin:
  pprof_enabled: true
log:
  level: warn
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	cfg, err := config.Load(nil, path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg.Server.Listen != ":7777" {
		t.Errorf("Server.Listen = %q, want %q", cfg.Server.Listen, ":7777")
	}
	if cfg.Server.ReadTimeout != 22*time.Second {
		t.Errorf("Server.ReadTimeout = %v, want %v",
			cfg.Server.ReadTimeout, 22*time.Second)
	}
	if !cfg.Admin.PprofEnabled {
		t.Errorf("Admin.PprofEnabled = false, want true")
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "warn")
	}
}

func TestLoad_MissingFileIsNotAnError(t *testing.T) {
	withRequired(t)

	cfg, err := config.Load(nil, "/definitely/not/a/real/path.yaml")
	if err != nil {
		t.Fatalf("Load() with missing file error = %v, want nil", err)
	}
	if cfg.Server.Listen != ":8080" {
		t.Errorf("defaults not applied: Server.Listen = %q", cfg.Server.Listen)
	}
}

func TestLoad_Precedence_FileEnvFlag(t *testing.T) {
	// file sets :7777, env sets :9090, flag sets :5555 -- flag wins.
	withRequired(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: \":7777\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("RFC_API_LISTEN", ":9090")

	cfg, err := config.Load([]string{"--listen", ":5555"}, path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Server.Listen != ":5555" {
		t.Errorf("Server.Listen = %q, want %q (flag should win)",
			cfg.Server.Listen, ":5555")
	}
}

func TestValidate_RequiredFieldsMissing(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T)
		wantStr string
	}{
		{
			name:    "no env set",
			setup:   func(t *testing.T) { t.Helper() },
			wantStr: "DATABASE_URL, MEILI_MASTER_KEY, RFC_API_WEBHOOK_SECRET",
		},
		{
			name: "only database set",
			setup: func(t *testing.T) {
				t.Helper()
				t.Setenv("DATABASE_URL", "postgres://x")
			},
			wantStr: "MEILI_MASTER_KEY, RFC_API_WEBHOOK_SECRET",
		},
		{
			name: "only meili missing",
			setup: func(t *testing.T) {
				t.Helper()
				t.Setenv("DATABASE_URL", "postgres://x")
				t.Setenv("RFC_API_WEBHOOK_SECRET", "s")
			},
			wantStr: "MEILI_MASTER_KEY",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			_, err := config.Load(nil, "")
			if err == nil {
				t.Fatal("Load() error = nil, want missing-fields error")
			}
			if !strings.Contains(err.Error(), tc.wantStr) {
				t.Errorf("Load() error = %q, want to contain %q",
					err.Error(), tc.wantStr)
			}
		})
	}
}

func TestLoad_MeiliEnv(t *testing.T) {
	withRequired(t)
	t.Setenv("MEILI_URL", "http://meili.svc:7700")
	t.Setenv("MEILI_API_KEY", "read-only-key")
	t.Setenv("MEILI_WRITE_KEY", "write-scoped-key")

	cfg, err := config.Load(nil, "")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Meili.URL != "http://meili.svc:7700" {
		t.Errorf("Meili.URL = %q", cfg.Meili.URL)
	}
	if cfg.Meili.ReadKey() != "read-only-key" {
		t.Errorf("ReadKey = %q, want read-only-key", cfg.Meili.ReadKey())
	}
	if cfg.Meili.WriteSecret() != "write-scoped-key" {
		t.Errorf("WriteSecret = %q, want write-scoped-key", cfg.Meili.WriteSecret())
	}
}

func TestMeili_KeyFallsBackToMaster(t *testing.T) {
	m := config.Meili{MasterKey: "master-only"}
	if got := m.ReadKey(); got != "master-only" {
		t.Errorf("ReadKey fallback = %q, want master-only", got)
	}
	if got := m.WriteSecret(); got != "master-only" {
		t.Errorf("WriteSecret fallback = %q, want master-only", got)
	}

	m.APIKey = "r"
	m.WriteKey = "w"
	if got := m.ReadKey(); got != "r" {
		t.Errorf("ReadKey explicit = %q, want r", got)
	}
	if got := m.WriteSecret(); got != "w" {
		t.Errorf("WriteSecret explicit = %q, want w", got)
	}
}

func TestLoad_MeiliURLDefault(t *testing.T) {
	withRequired(t)
	cfg, err := config.Load(nil, "")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Meili.URL != "http://localhost:7700" {
		t.Errorf("Meili.URL default = %q, want http://localhost:7700", cfg.Meili.URL)
	}
}

func TestLoad_FlagParseError(t *testing.T) {
	withRequired(t)
	_, err := config.Load([]string{"--not-a-real-flag"}, "")
	if err == nil {
		t.Fatal("Load() error = nil, want flag parse error")
	}
}
