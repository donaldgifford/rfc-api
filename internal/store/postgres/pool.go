// Package postgres is the PostgreSQL-backed implementation of the
// rfc-api store seam. Interfaces live in internal/store; this package
// never imports the service or handler layers.
//
// The pool is constructed once at serve-time and shared across every
// store method call. Methods take a parent context and run their SQL
// inside a single read-only transaction — write paths belong to
// IMPL-0003 (sync worker) and are out of scope here.
package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool-tuning defaults per IMPL-0002 RD3. Surfaced as constants so
// they're easy to find and tweak. None are exposed as env vars yet —
// the first operator-facing knob that appears gets a config field.
const (
	defaultMaxConns          = 25
	defaultMinConns          = 5
	defaultMaxConnIdleTime   = 5 * time.Minute
	defaultHealthCheckPeriod = 30 * time.Second
)

// NewPool opens a pgxpool against the given DATABASE_URL and verifies
// the connection with a single Ping. The caller owns the pool and must
// call Close on shutdown.
//
// The logger receives one INFO line on success carrying the server
// version and the effective pool settings so operators can confirm
// config in-place without shelling into the pod.
func NewPool(ctx context.Context, databaseURL string, logger *slog.Logger) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	cfg.MaxConns = defaultMaxConns
	cfg.MinConns = defaultMinConns
	cfg.MaxConnIdleTime = defaultMaxConnIdleTime
	cfg.HealthCheckPeriod = defaultHealthCheckPeriod

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	logger.InfoContext(ctx, "postgres pool ready",
		"server_version", serverVersion(ctx, pool),
		"max_conns", cfg.MaxConns,
		"min_conns", cfg.MinConns,
		"max_conn_idle_time", cfg.MaxConnIdleTime.String(),
		"health_check_period", cfg.HealthCheckPeriod.String(),
	)

	return pool, nil
}

// serverVersion returns the Postgres server version string or the
// empty string if the query fails. Best-effort for the startup log;
// a failure here does not block the pool from being returned.
func serverVersion(ctx context.Context, pool *pgxpool.Pool) string {
	var v string
	if err := pool.QueryRow(ctx, "SHOW server_version").Scan(&v); err != nil {
		return ""
	}
	return v
}
