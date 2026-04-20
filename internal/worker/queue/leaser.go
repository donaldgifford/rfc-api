package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Handler processes a single job. The returned error, if any, is
// the cause string persisted on dead-letter.
type Handler func(ctx context.Context, job Job) error

// KindConfig binds a job kind to its handler and concurrency budget.
// A per-kind semaphore prevents one noisy kind from starving others
// that share the same pool.
type KindConfig struct {
	Handler     Handler
	Concurrency int
}

// Leaser polls a Queue on a fixed interval, leasing up to N jobs
// per tick and dispatching to the per-kind Handler. Metrics is
// optional; when non-nil, per-kind counters + histograms fire on
// each job's lifecycle.
type Leaser struct {
	queue    *Queue
	workerID string
	kinds    map[string]KindConfig
	interval time.Duration
	logger   *slog.Logger
	metrics  LeaseMetrics
}

// LeaseMetrics is the narrow surface the leaser pushes to. Kept as
// an interface so the worker can wire an obs.Metrics-backed type
// without the queue pkg importing prometheus directly.
type LeaseMetrics interface {
	JobLeased(kind string)
	JobCompleted(kind, result string, duration time.Duration)
	JobDead(kind string)
}

// NoopMetrics satisfies LeaseMetrics with no-ops; useful in tests.
type NoopMetrics struct{}

// JobLeased implements LeaseMetrics.
func (NoopMetrics) JobLeased(string) {}

// JobCompleted implements LeaseMetrics.
func (NoopMetrics) JobCompleted(string, string, time.Duration) {}

// JobDead implements LeaseMetrics.
func (NoopMetrics) JobDead(string) {}

// LeaserOptions wires a Leaser. WorkerID is surfaced in jobs.locked_by
// for debugging (hostname + pid + uuid is a reasonable default set by
// the worker). Interval defaults to 2s (matching cfg.ProcessorPoll).
type LeaserOptions struct {
	Queue    *Queue
	WorkerID string
	Kinds    map[string]KindConfig
	Interval time.Duration
	Logger   *slog.Logger
	Metrics  LeaseMetrics
}

// NewLeaser returns a Leaser. Call Run(ctx) to start polling.
func NewLeaser(opts *LeaserOptions) (*Leaser, error) {
	if opts == nil || opts.Queue == nil {
		return nil, errors.New("queue.NewLeaser: nil queue")
	}
	if opts.WorkerID == "" {
		return nil, errors.New("queue.NewLeaser: WorkerID is required")
	}
	if len(opts.Kinds) == 0 {
		return nil, errors.New("queue.NewLeaser: at least one kind required")
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	metrics := opts.Metrics
	if metrics == nil {
		metrics = NoopMetrics{}
	}
	return &Leaser{
		queue:    opts.Queue,
		workerID: opts.WorkerID,
		kinds:    opts.Kinds,
		interval: interval,
		logger:   logger.With("component", "queue-leaser"),
		metrics:  metrics,
	}, nil
}

// Run polls the queue until ctx is canceled. Each tick leases up to
// sum(concurrency) jobs, dispatches them to per-kind handlers under
// a per-kind semaphore, and waits for the next tick.
//
// Returns nil on ctx cancel, the poll error otherwise.
func (l *Leaser) Run(ctx context.Context) error {
	sem := l.buildSemaphores()
	t := time.NewTicker(l.interval)
	defer t.Stop()

	var wg sync.WaitGroup
	defer wg.Wait() // drain in-flight handlers before returning

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := l.tick(ctx, sem, &wg); err != nil {
				l.logger.ErrorContext(ctx, "leaser tick", "err", err.Error())
			}
		}
	}
}

// tick leases up to one batch and dispatches. Per-kind concurrency
// is enforced with a buffered channel acting as a semaphore.
func (l *Leaser) tick(ctx context.Context, sem map[string]chan struct{}, wg *sync.WaitGroup) error {
	kinds := l.kindList()
	n := l.totalBudget()

	jobs, err := l.queue.Lease(ctx, l.workerID, kinds, n)
	if err != nil {
		return fmt.Errorf("lease: %w", err)
	}

	for i := range jobs {
		job := &jobs[i]
		cfg, ok := l.kinds[job.Kind]
		if !ok {
			// Unknown kind — let it dead-letter immediately so the
			// operator sees a configuration mismatch. We can't handle
			// it; don't hold the lease hostage.
			err := fmt.Errorf("leaser: no handler for kind %q", job.Kind)
			if fErr := l.queue.Fail(ctx, job.ID, l.queue.maxAttempt+1, err); fErr != nil {
				l.logger.ErrorContext(ctx, "fail unknown-kind", "id", job.ID, "err", fErr.Error())
			}
			continue
		}

		slot := sem[job.Kind]
		select {
		case slot <- struct{}{}:
		default:
			// Over budget for this kind on this tick — release the
			// lease so another worker can grab it.
			if fErr := l.queue.Fail(ctx, job.ID, job.Attempts-1,
				errors.New("leaser: concurrency cap")); fErr != nil {
				l.logger.ErrorContext(ctx, "fail over-cap", "id", job.ID, "err", fErr.Error())
			}
			continue
		}

		wg.Add(1)
		l.metrics.JobLeased(job.Kind)
		go l.dispatch(ctx, job, cfg.Handler, slot, wg)
	}
	return nil
}

// dispatch runs one job under a recovered handler, then reports
// success or failure to the queue.
func (l *Leaser) dispatch(ctx context.Context, job *Job, handler Handler, slot chan struct{}, wg *sync.WaitGroup) {
	start := time.Now()
	defer wg.Done()
	defer func() { <-slot }()

	result := l.finalize(ctx, job, runHandler(ctx, job, handler))
	l.metrics.JobCompleted(job.Kind, result, time.Since(start))
}

// finalize persists the handler outcome and returns the metrics
// result label.
func (l *Leaser) finalize(ctx context.Context, job *Job, handlerErr error) string {
	if handlerErr == nil {
		if sErr := l.queue.Succeed(ctx, job.ID); sErr != nil {
			l.logger.ErrorContext(ctx, "queue succeed", "id", job.ID, "err", sErr.Error())
		}
		return "ok"
	}
	if fErr := l.queue.Fail(ctx, job.ID, job.Attempts, handlerErr); fErr != nil {
		l.logger.ErrorContext(ctx, "queue fail", "id", job.ID, "err", fErr.Error())
	}
	if job.Attempts >= l.queue.maxAttempt {
		l.metrics.JobDead(job.Kind)
		return "dead"
	}
	return "error"
}

// runHandler runs h with panic recovery — a panicking handler turns
// into a failed job, not a dead worker process.
func runHandler(ctx context.Context, job *Job, h Handler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return h(ctx, *job)
}

func (l *Leaser) buildSemaphores() map[string]chan struct{} {
	out := make(map[string]chan struct{}, len(l.kinds))
	for kind, cfg := range l.kinds {
		n := cfg.Concurrency
		if n <= 0 {
			n = 1
		}
		out[kind] = make(chan struct{}, n)
	}
	return out
}

func (l *Leaser) kindList() []string {
	out := make([]string, 0, len(l.kinds))
	for k := range l.kinds {
		out = append(out, k)
	}
	return out
}

func (l *Leaser) totalBudget() int {
	sum := 0
	for _, cfg := range l.kinds {
		n := cfg.Concurrency
		if n <= 0 {
			n = 1
		}
		sum += n
	}
	return sum
}

// WorkerID returns a stable identifier for this process useful as
// the locked_by column. Format: hostname/pid/uuid-short so multiple
// processes on one box are distinguishable and unique across runs.
func WorkerID(hostname string, pid int) string {
	u := uuid.New().String()
	return fmt.Sprintf("%s/%d/%s", hostname, pid, u[:8])
}
