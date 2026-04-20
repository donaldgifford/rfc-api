//go:build integration

package postgres_test

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/store/postgres"
)

func TestProbe_Check_OK(t *testing.T) {
	pool := testPool(t)
	p := postgres.Probe{Pool: pool}

	if got := p.Name(); got != "postgres" {
		t.Fatalf("Name() = %q, want %q", got, "postgres")
	}
	if err := p.Check(t.Context()); err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
}

// TestProbe_Check_ClosedPool_Errors asserts the probe flips to
// unhealthy when the pool is unusable. Uses a private pool (not
// testPool) so the shared truncate cleanup doesn't race with the
// close we issue ourselves.
func TestProbe_Check_ClosedPool_Errors(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := postgres.NewPool(t.Context(), dsn, logger)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	pool.Close()

	p := postgres.Probe{Pool: pool}
	if err := p.Check(t.Context()); err == nil {
		t.Fatal("Check() against a closed pool returned nil; want error")
	}
}
