//go:build integration

package postgres_test

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/store/postgres"
)

// TestNewPool_Roundtrip opens a pool against the DATABASE_URL exposed
// by the CI service / compose stack, issues a Ping (via NewPool's
// built-in verification), and closes cleanly.
//
// Gated with `//go:build integration` so `make test` without docker
// stays green; CI runs `make test-integration` (added in Phase 4).
func TestNewPool_Roundtrip(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}

	// A silent logger — we don't want test output polluted with the
	// startup INFO line on every run.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	pool, err := postgres.NewPool(t.Context(), dsn, logger)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	var one int
	if err := pool.QueryRow(t.Context(), "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if one != 1 {
		t.Fatalf("SELECT 1 returned %d, want 1", one)
	}
}
