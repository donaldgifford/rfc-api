// Package config loads rfc-api runtime configuration from four sources in
// increasing precedence: built-in defaults, an optional YAML file, env
// vars, and CLI flags. Env-var naming follows the rule in DESIGN-0001
// §Configuration: service-prefixed (RFC_API_*) for config we define,
// upstream-standard names (DATABASE_URL, MEILI_MASTER_KEY,
// OTEL_EXPORTER_OTLP_ENDPOINT) for variables defined by external deps.
//
// os.Getenv calls are restricted to this package (enforced by a test).
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultFilePath is the conventional location of the optional YAML
// config file. If the file does not exist, Load silently falls back to
// defaults + env + flags.
const DefaultFilePath = "/etc/rfc-api/config.yaml"

// Config aggregates every runtime knob. Loaded by Load.
type Config struct {
	Server          Server        `yaml:"server"`
	Admin           Admin         `yaml:"admin"`
	Log             Log           `yaml:"log"`
	RateLimit       RateLimit     `yaml:"rate_limit"`
	Database        Database      `yaml:"database"`
	Meili           Meili         `yaml:"meili"`
	OTel            OTel          `yaml:"otel"`
	Webhook         Webhook       `yaml:"webhook"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// Server holds main-port HTTP settings.
type Server struct {
	Listen       string        `yaml:"listen"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	CORSOrigins  []string      `yaml:"cors_origins"`
}

// Admin holds admin-port HTTP settings.
//
// Admin has no write timeout on purpose: pprof CPU profile is a
// long-running endpoint and a write timeout would terminate the
// profile mid-capture.
type Admin struct {
	Listen       string        `yaml:"listen"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	PprofEnabled bool          `yaml:"pprof_enabled"`
}

// Log holds slog handler settings.
type Log struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // json | text
}

// RateLimit holds v1-chain rate limiter settings. KeyFunc is pluggable
// at construction time (not config) per Resolved Decision 3.
type RateLimit struct {
	RPS   int           `yaml:"rps"`
	Burst int           `yaml:"burst"`
	TTL   time.Duration `yaml:"ttl"`
}

// Database holds the DATABASE_URL (upstream-standard name).
type Database struct {
	URL string `yaml:"url"`
}

// Meili holds the MEILI_MASTER_KEY (Meilisearch's own env var name).
type Meili struct {
	MasterKey string `yaml:"master_key"`
}

// OTel holds OpenTelemetry settings.
//
// OTLPEndpoint is read from OTEL_EXPORTER_OTLP_ENDPOINT (standard OTel
// env var). When empty, tracing is a no-op.
type OTel struct {
	OTLPEndpoint     string  `yaml:"otlp_endpoint"`
	TraceSampleRatio float64 `yaml:"trace_sample_ratio"`
}

// Webhook holds GitHub webhook settings.
type Webhook struct {
	Secret string `yaml:"secret"`
}

// Load returns a populated Config, applying the precedence
// defaults < file < env < flags. args should be os.Args[1:] minus the
// subcommand (e.g. os.Args[2:]).
//
// filePath may be empty, in which case no file is read. Missing files
// are not an error -- config files are optional.
func Load(args []string, filePath string) (*Config, error) {
	cfg := defaults()

	if filePath != "" {
		if err := loadFile(cfg, filePath); err != nil {
			return nil, fmt.Errorf("load file %q: %w", filePath, err)
		}
	}

	loadEnv(cfg)

	if err := loadFlags(cfg, args); err != nil {
		return nil, fmt.Errorf("parse flags: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// defaults returns the built-in defaults, safe for local dev except
// for the three required fields (DATABASE_URL, MEILI_MASTER_KEY,
// RFC_API_WEBHOOK_SECRET) which must be set via env/file/flag.
func defaults() *Config {
	return &Config{
		Server: Server{
			Listen:       ":8080",
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		Admin: Admin{
			Listen:       "127.0.0.1:8081",
			ReadTimeout:  5 * time.Second,
			PprofEnabled: false,
		},
		Log: Log{
			Level:  "info",
			Format: "json",
		},
		RateLimit: RateLimit{
			RPS:   50,
			Burst: 100,
			TTL:   time.Hour,
		},
		OTel: OTel{
			TraceSampleRatio: 0.1,
		},
		ShutdownTimeout: 20 * time.Second,
	}
}

// loadFile reads YAML from path into cfg (destructive merge: explicit
// keys override defaults). A missing file is not an error.
func loadFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	return nil
}

// loadEnv pulls config values from the process environment. Each var
// is read explicitly (no reflection) so the mapping is greppable and
// the env-var naming rule is visible in one place.
func loadEnv(cfg *Config) {
	// -- rfc-api-owned (service-prefixed) --------------------------
	setString(&cfg.Server.Listen, "RFC_API_LISTEN")
	setDuration(&cfg.Server.ReadTimeout, "RFC_API_READ_TIMEOUT")
	setDuration(&cfg.Server.WriteTimeout, "RFC_API_WRITE_TIMEOUT")
	setCommaList(&cfg.Server.CORSOrigins, "RFC_API_CORS_ORIGINS")

	setString(&cfg.Admin.Listen, "RFC_API_ADMIN_LISTEN")
	setBool(&cfg.Admin.PprofEnabled, "RFC_API_PPROF_ENABLED")

	setString(&cfg.Log.Level, "RFC_API_LOG_LEVEL")
	setString(&cfg.Log.Format, "RFC_API_LOG_FORMAT")

	setInt(&cfg.RateLimit.RPS, "RFC_API_RATE_LIMIT_RPS")
	setInt(&cfg.RateLimit.Burst, "RFC_API_RATE_LIMIT_BURST")
	setDuration(&cfg.RateLimit.TTL, "RFC_API_RATE_LIMIT_TTL")

	setFloat(&cfg.OTel.TraceSampleRatio, "RFC_API_TRACE_SAMPLE_RATIO")

	setString(&cfg.Webhook.Secret, "RFC_API_WEBHOOK_SECRET")

	setDuration(&cfg.ShutdownTimeout, "RFC_API_SHUTDOWN_TIMEOUT")

	// -- upstream-standard names (see DESIGN-0001 §Configuration) --
	setString(&cfg.Database.URL, "DATABASE_URL")
	setString(&cfg.Meili.MasterKey, "MEILI_MASTER_KEY")
	setString(&cfg.OTel.OTLPEndpoint, "OTEL_EXPORTER_OTLP_ENDPOINT")
}

// loadFlags binds CLI flags to cfg. Flags take highest precedence.
//
// Flag names mirror the env-var shape in lowercase with hyphens
// (RFC_API_LISTEN -> --listen; DATABASE_URL -> --database-url).
func loadFlags(cfg *Config, args []string) error {
	fs := flag.NewFlagSet("rfc-api", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // tests + help rendering handle output

	fs.StringVar(&cfg.Server.Listen, "listen", cfg.Server.Listen, "main HTTP listen address")
	fs.StringVar(&cfg.Admin.Listen, "admin-listen", cfg.Admin.Listen, "admin HTTP listen address")
	fs.BoolVar(&cfg.Admin.PprofEnabled, "pprof", cfg.Admin.PprofEnabled, "enable /debug/pprof/* on the admin port")
	fs.StringVar(&cfg.Log.Level, "log-level", cfg.Log.Level, "log level (debug|info|warn|error)")
	fs.StringVar(&cfg.Log.Format, "log-format", cfg.Log.Format, "log format (json|text)")
	fs.StringVar(&cfg.Database.URL, "database-url", cfg.Database.URL, "Postgres DSN")
	fs.StringVar(&cfg.Meili.MasterKey, "meili-master-key", cfg.Meili.MasterKey, "Meilisearch master key")
	fs.StringVar(&cfg.OTel.OTLPEndpoint, "otel-endpoint", cfg.OTel.OTLPEndpoint, "OTLP collector endpoint")

	if err := fs.Parse(args); err != nil {
		return err //nolint:wrapcheck // direct flag.Parse errors are user-facing
	}
	return nil
}

// Validate returns an error naming the missing required field. Clear
// messages are the point; a one-line log at startup beats digging
// through a config reference.
func (c *Config) Validate() error {
	var missing []string
	if c.Database.URL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if c.Meili.MasterKey == "" {
		missing = append(missing, "MEILI_MASTER_KEY")
	}
	if c.Webhook.Secret == "" {
		missing = append(missing, "RFC_API_WEBHOOK_SECRET")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config: %s",
			strings.Join(missing, ", "))
	}
	return nil
}

// -- typed env setters ------------------------------------------------

func setString(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func setBool(dst *bool, key string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	if b, err := strconv.ParseBool(v); err == nil {
		*dst = b
	}
}

func setInt(dst *int, key string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	if n, err := strconv.Atoi(v); err == nil {
		*dst = n
	}
}

func setFloat(dst *float64, key string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		*dst = f
	}
}

func setDuration(dst *time.Duration, key string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	if d, err := time.ParseDuration(v); err == nil {
		*dst = d
	}
}

func setCommaList(dst *[]string, key string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	if v == "" {
		*dst = nil
		return
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	*dst = out
}
