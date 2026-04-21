//go:build integration

package queue_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/donaldgifford/rfc-api/db"
	"github.com/donaldgifford/rfc-api/internal/store/postgres"
	"github.com/donaldgifford/rfc-api/internal/worker/queue"
)

// testPool returns a pool with jobs truncated so each test starts
// from a known clean state.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}

	// Idempotent migrate-up — the pool integration tests share the
	// same schema TestMain pattern as test/integration/postgres but
	// this package has no TestMain.
	if err := runMigrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := postgres.NewPool(t.Context(), dsn, logger)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	_, err = pool.Exec(context.Background(), `TRUNCATE TABLE jobs RESTART IDENTITY`)
	if err != nil {
		t.Fatalf("truncate jobs: %v", err)
	}
	return pool
}

func runMigrate(dsn string) error {
	m, err := db.NewMigrator(dsn)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func TestEnqueue_Idempotent(t *testing.T) {
	pool := testPool(t)
	q := queue.New(pool, queue.Options{})

	payload := map[string]string{"path": "docs/0001.md"}
	if err := q.Enqueue(t.Context(), "ingest", "content:abc", payload, time.Time{}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	later := time.Now().Add(time.Minute)
	if err := q.Enqueue(t.Context(), "ingest", "content:abc", payload, later); err != nil {
		t.Fatalf("second enqueue: %v", err)
	}

	var count int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM jobs WHERE kind='ingest' AND dedup_key='content:abc'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("want 1 row, got %d", count)
	}
}

func TestLease_SkipLocked_TwoWorkers(t *testing.T) {
	pool := testPool(t)
	q := queue.New(pool, queue.Options{})

	// Seed 3 queued jobs.
	for i := range 3 {
		if err := q.Enqueue(t.Context(), "reindex",
			uuid.NewString(), map[string]int{"i": i}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}

	first, err := q.Lease(t.Context(), "worker-a", []string{"reindex"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("worker-a got %d, want 2", len(first))
	}

	// The second worker should skip the locked rows and get exactly
	// the remaining one.
	second, err := q.Lease(t.Context(), "worker-b", []string{"reindex"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("worker-b got %d, want 1", len(second))
	}

	// Finally, no duplicates across workers.
	seen := make(map[uuid.UUID]bool)
	for _, j := range append(first, second...) {
		if seen[j.ID] {
			t.Errorf("duplicate id leased: %s", j.ID)
		}
		seen[j.ID] = true
	}
}

func TestFail_BacksOffAndRequeues(t *testing.T) {
	pool := testPool(t)
	// Large max to make this deterministic — Fail on attempts < max
	// always requeues, never promotes to dead.
	q := queue.New(pool, queue.Options{
		MaxAttempts: 10,
		BaseDelay:   time.Millisecond,
		MaxDelay:    10 * time.Millisecond,
	})

	if err := q.Enqueue(t.Context(), "ingest", "retry", map[string]int{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	leased, err := q.Lease(t.Context(), "worker", []string{"ingest"}, 1)
	if err != nil || len(leased) != 1 {
		t.Fatalf("lease: %v, len=%d", err, len(leased))
	}
	j := leased[0]
	before := time.Now()

	if err := q.Fail(t.Context(), j.ID, j.Attempts, errors.New("boom")); err != nil {
		t.Fatalf("fail: %v", err)
	}

	var state string
	var runAfter time.Time
	if err := pool.QueryRow(t.Context(),
		`SELECT state, run_after FROM jobs WHERE id=$1`, j.ID).Scan(&state, &runAfter); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Errorf("state = %q, want queued", state)
	}
	// The backoff is full-jitter over [0, base*2^(attempt-1)] so it
	// may be zero; assert only that run_after didn't move backward.
	if runAfter.Before(before.Add(-time.Second)) {
		t.Errorf("run_after=%v is well before before=%v", runAfter, before)
	}
}

func TestFail_DeadLetterAfterMaxAttempts(t *testing.T) {
	pool := testPool(t)
	// Zero-ish backoff so lease-fail loops are immediate and the
	// test doesn't race the ticker/clock.
	q := queue.New(pool, queue.Options{
		MaxAttempts: 3,
		BaseDelay:   time.Microsecond,
		MaxDelay:    time.Microsecond,
	})

	if err := q.Enqueue(t.Context(), "ingest", "doomed", map[string]int{}, time.Time{}); err != nil {
		t.Fatal(err)
	}

	// Loop until we drain or exceed the attempt budget. Guard with
	// a hard cap so a runaway never hangs CI.
	for iter := 0; iter < 10; iter++ {
		// Bounce the job forward explicitly so backoff jitter can't
		// keep it invisible to Lease.
		if _, err := pool.Exec(t.Context(),
			`UPDATE jobs SET run_after = now() - interval '1 second' WHERE kind='ingest' AND dedup_key='doomed' AND state='queued'`); err != nil {
			t.Fatal(err)
		}
		leased, err := q.Lease(t.Context(), "worker", []string{"ingest"}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(leased) == 0 {
			break // job promoted to dead; Lease only sees 'queued'
		}
		if err := q.Fail(t.Context(), leased[0].ID, leased[0].Attempts, errors.New("nope")); err != nil {
			t.Fatalf("fail iter %d: %v", iter, err)
		}
	}

	var state string
	var payload json.RawMessage
	err := pool.QueryRow(t.Context(),
		`SELECT state, payload FROM jobs WHERE kind='ingest' AND dedup_key='doomed'`).
		Scan(&state, &payload)
	if err != nil {
		t.Fatal(err)
	}
	if state != "dead" {
		t.Fatalf("state = %q, want dead", state)
	}
	if !strings.Contains(string(payload), `"_last_error"`) {
		t.Errorf("payload missing _last_error: %s", payload)
	}
}

func TestSucceed_DeletesRow(t *testing.T) {
	pool := testPool(t)
	q := queue.New(pool, queue.Options{})

	if err := q.Enqueue(t.Context(), "ingest", "ok", map[string]int{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	leased, _ := q.Lease(t.Context(), "worker", []string{"ingest"}, 1)
	if err := q.Succeed(t.Context(), leased[0].ID); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM jobs WHERE kind='ingest' AND dedup_key='ok'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("row not deleted; count=%d", count)
	}
}

func TestDepth(t *testing.T) {
	pool := testPool(t)
	q := queue.New(pool, queue.Options{})

	_ = q.Enqueue(t.Context(), "ingest", "a", 1, time.Time{})
	_ = q.Enqueue(t.Context(), "ingest", "b", 1, time.Time{})
	_ = q.Enqueue(t.Context(), "reindex", "x", 1, time.Time{})

	d, err := q.Depth(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if d[queue.DepthKey{Kind: "ingest", State: "queued"}] != 2 {
		t.Errorf("ingest/queued depth = %d, want 2", d[queue.DepthKey{Kind: "ingest", State: "queued"}])
	}
	if d[queue.DepthKey{Kind: "reindex", State: "queued"}] != 1 {
		t.Errorf("reindex/queued depth = %d", d[queue.DepthKey{Kind: "reindex", State: "queued"}])
	}
}

// TestLeaser_DispatchesAndSucceeds drives the leaser end-to-end: one
// kind, one handler, a couple of jobs. Covers the happy path +
// panic recovery + dead-letter in a single test so the test count
// is proportional to real behavior, not internal steps.
func TestLeaser_PanicRecoveryAndDeadLetter(t *testing.T) {
	pool := testPool(t)
	q := queue.New(pool, queue.Options{MaxAttempts: 1, BaseDelay: time.Millisecond})

	_ = q.Enqueue(t.Context(), "ingest", "panic-me", map[string]int{}, time.Time{})

	var handled atomic.Int32
	leaser, err := queue.NewLeaser(&queue.LeaserOptions{
		Queue:    q,
		WorkerID: "test/1/abc",
		Interval: 10 * time.Millisecond,
		Kinds: map[string]queue.KindConfig{
			"ingest": {
				Concurrency: 1,
				Handler: func(_ context.Context, _ queue.Job) error {
					handled.Add(1)
					panic("explode")
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- leaser.Run(ctx) }()

	// Wait for the job to be handled + moved to dead.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		_ = pool.QueryRow(t.Context(),
			`SELECT state FROM jobs WHERE kind='ingest' AND dedup_key='panic-me'`).Scan(&state)
		if state == "dead" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("leaser.Run: %v", err)
	}

	if handled.Load() == 0 {
		t.Error("handler never ran")
	}

	var state string
	if err := pool.QueryRow(t.Context(),
		`SELECT state FROM jobs WHERE kind='ingest' AND dedup_key='panic-me'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "dead" {
		t.Errorf("state = %q, want dead", state)
	}
}
