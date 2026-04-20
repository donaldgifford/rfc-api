// Package queue is the Postgres-backed job queue for the rfc-api
// worker. Jobs are rows in the `jobs` table (IMPL-0002 Phase 1);
// Lease uses `FOR UPDATE SKIP LOCKED` so N workers can coordinate
// without an external broker (RFC-0001 Sync).
//
// Job semantics:
//   - `Enqueue` inserts with `(kind, dedup_key)` unique. A collision
//     bumps `run_after` rather than failing, so a re-emitted webhook
//     or scanner hit is an idempotent re-queue.
//   - `Lease` atomically selects N queued jobs whose `run_after` has
//     passed, flips them to `leased`, records the worker id, and
//     bumps `attempts`.
//   - `Succeed` deletes the row (IMPL-0003 RD5 — no retention).
//   - `Fail` either bumps `run_after` with exponential backoff or
//     promotes to `state='dead'` once the attempt budget is blown.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Job is one leased unit of work. Payload is the raw JSON body as
// stored; handlers unmarshal into the concrete shape they expect.
type Job struct {
	ID       uuid.UUID
	Kind     string
	DedupKey string
	Payload  json.RawMessage
	Attempts int
}

// Queue is the pool-backed queue seam.
type Queue struct {
	pool       *pgxpool.Pool
	maxAttempt int
	baseDelay  time.Duration
	maxDelay   time.Duration
}

// Options tunes retry behavior. Zero values map to the RD6 defaults:
// 5 attempts, exponential with jitter, cap at 30 minutes.
type Options struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// New builds a Queue with IMPL-0003 RD6 retry defaults.
func New(pool *pgxpool.Pool, opts Options) *Queue {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 5
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = time.Second
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = 30 * time.Minute
	}
	return &Queue{
		pool:       pool,
		maxAttempt: opts.MaxAttempts,
		baseDelay:  opts.BaseDelay,
		maxDelay:   opts.MaxDelay,
	}
}

// Enqueue inserts a job or bumps `run_after` on an existing one.
// payload is serialized here — callers pass their own concrete
// struct and the queue owns JSON marshaling. runAfter is when the
// job becomes eligible for lease; zero means "now".
func (q *Queue) Enqueue(ctx context.Context, kind, dedupKey string, payload any, runAfter time.Time) error {
	if kind == "" {
		return errors.New("queue.Enqueue: kind is required")
	}
	if dedupKey == "" {
		return errors.New("queue.Enqueue: dedup_key is required")
	}
	if runAfter.IsZero() {
		runAfter = time.Now()
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	// ON CONFLICT bumps run_after when the caller wants to delay a
	// re-queue; otherwise the row stays queued with its original
	// timestamp so the in-flight attempt count is preserved.
	const stmt = `
		INSERT INTO jobs (kind, dedup_key, payload, run_after)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (kind, dedup_key) DO UPDATE
			SET run_after = GREATEST(jobs.run_after, EXCLUDED.run_after),
			    updated_at = now()
	`
	if _, err := q.pool.Exec(ctx, stmt, kind, dedupKey, body, runAfter); err != nil {
		return fmt.Errorf("enqueue %s/%s: %w", kind, dedupKey, err)
	}
	return nil
}

// Lease claims up to n jobs matching any of kinds for workerID.
// Rows are atomically transitioned to `leased`. A caller that
// crashes before Succeed/Fail will leak the lease; a future janitor
// (out of scope here) can recover jobs whose `locked_at` is ancient.
func (q *Queue) Lease(ctx context.Context, workerID string, kinds []string, n int) ([]Job, error) {
	if n <= 0 {
		return nil, errors.New("queue.Lease: n must be positive")
	}
	if workerID == "" {
		return nil, errors.New("queue.Lease: workerID is required")
	}
	if len(kinds) == 0 {
		return nil, nil
	}

	const stmt = `
		WITH claimed AS (
			SELECT id FROM jobs
			WHERE state = 'queued'
			  AND run_after <= now()
			  AND kind = ANY($1)
			ORDER BY run_after, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE jobs SET
			state      = 'leased',
			locked_by  = $3,
			locked_at  = now(),
			attempts   = attempts + 1,
			updated_at = now()
		WHERE id IN (SELECT id FROM claimed)
		RETURNING id, kind, dedup_key, payload, attempts
	`
	rows, err := q.pool.Query(ctx, stmt, kinds, n, workerID)
	if err != nil {
		return nil, fmt.Errorf("lease: %w", err)
	}
	defer rows.Close()

	jobs := make([]Job, 0, n)
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Kind, &j.DedupKey, &j.Payload, &j.Attempts); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return jobs, nil
}

// Succeed deletes the job row. No retention per RD5.
func (q *Queue) Succeed(ctx context.Context, id uuid.UUID) error {
	if _, err := q.pool.Exec(ctx, `DELETE FROM jobs WHERE id = $1`, id); err != nil {
		return fmt.Errorf("succeed %s: %w", id, err)
	}
	return nil
}

// Fail re-queues with exponential backoff, or promotes to `dead`
// once attempts exceed MaxAttempts. cause is stored in the payload
// only when the promotion happens (dead-letter breadcrumb).
func (q *Queue) Fail(ctx context.Context, id uuid.UUID, attempts int, cause error) error {
	if attempts >= q.maxAttempt {
		const stmt = `
			UPDATE jobs SET
				state      = 'dead',
				locked_by  = NULL,
				locked_at  = NULL,
				updated_at = now(),
				payload    = jsonb_set(payload, '{_last_error}', to_jsonb($2::text), true)
			WHERE id = $1
		`
		if _, err := q.pool.Exec(ctx, stmt, id, errString(cause)); err != nil {
			return fmt.Errorf("dead-letter %s: %w", id, err)
		}
		return nil
	}

	next := q.backoff(attempts)
	const stmt = `
		UPDATE jobs SET
			state      = 'queued',
			locked_by  = NULL,
			locked_at  = NULL,
			run_after  = now() + ($2::bigint * interval '1 millisecond'),
			updated_at = now()
		WHERE id = $1
	`
	if _, err := q.pool.Exec(ctx, stmt, id, next.Milliseconds()); err != nil {
		return fmt.Errorf("requeue %s: %w", id, err)
	}
	return nil
}

// backoff returns the next delay for an attempt. Exponential base-2
// with full jitter, capped at maxDelay.
func (q *Queue) backoff(attempt int) time.Duration {
	// attempt is 1-indexed by the time Fail is called.
	exp := math.Pow(2, float64(attempt-1))
	raw := time.Duration(exp) * q.baseDelay
	if raw <= 0 || raw > q.maxDelay {
		raw = q.maxDelay
	}
	// Full jitter: uniform in [0, raw].
	return time.Duration(rand.Int64N(int64(raw) + 1)) //nolint:gosec // jitter, not security
}

// Depth returns a map of (kind,state) → row count, for the
// `queue_depth` Prometheus gauge. Callers sample this periodically
// from the worker's admin scrape path.
func (q *Queue) Depth(ctx context.Context) (map[DepthKey]int, error) {
	const stmt = `SELECT kind, state, count(*) FROM jobs GROUP BY kind, state`
	rows, err := q.pool.Query(ctx, stmt)
	if err != nil {
		return nil, fmt.Errorf("depth: %w", err)
	}
	defer rows.Close()

	out := make(map[DepthKey]int, 8)
	for rows.Next() {
		var k, s string
		var n int
		if err := rows.Scan(&k, &s, &n); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out[DepthKey{Kind: k, State: s}] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// DepthKey pairs kind + state for the per-kind gauge.
type DepthKey struct {
	Kind  string
	State string
}

// IsUniqueViolation returns true when err is a pgconn "unique
// constraint" error. Useful to callers that want to inspect an
// Enqueue race (we don't currently surface one since ON CONFLICT
// handles it, but handlers can use this against their own inserts).
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// errString is Small and boring: nil → "", otherwise err.Error().
// Kept here so the dead-letter SQL stays readable.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
