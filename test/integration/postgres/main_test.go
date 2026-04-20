//go:build integration

// Package postgres_test exercises the full rfc-api HTTP stack
// (registry → service → postgres store → handlers) against a real
// Postgres instance. The suite is gated with the `integration` build
// tag so `make test` remains Docker-free; CI runs it via
// `make test-integration` with DATABASE_URL pointed at a service
// container.
package postgres_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/donaldgifford/rfc-api/db"
	"github.com/donaldgifford/rfc-api/internal/store/postgres"
)

// TestMain prepares the shared database once per test binary run:
// applies the embedded migrations to DATABASE_URL so every test
// below assumes an up-to-date schema. Tests own row-level isolation
// via truncate().
func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// Leave the skip decision to each test — some environments
		// run the binary solely for lint / vet with no DB available.
		os.Exit(m.Run())
	}

	if err := migrateUp(dsn); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func migrateUp(dsn string) error {
	mig, err := db.NewMigrator(dsn)
	if err != nil {
		return err
	}
	defer func() { _, _ = mig.Close() }()

	if err := mig.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := postgres.NewPool(t.Context(), dsn, logger)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	truncate(t, pool)
	t.Cleanup(func() { truncate(t, pool) })
	return pool
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `TRUNCATE TABLE
		discussion_participants,
		discussions,
		links,
		authors,
		documents,
		jobs
	RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}
