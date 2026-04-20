// Package worker hosts the `rfc-api work` long-running process: the
// scanner loop that enumerates configured source repos, the processor
// loop that leases and runs jobs from the Postgres-backed queue, and
// a small admin port for kubelet probes + metrics.
//
// Per IMPL-0003 the worker is a single replica in v1. Lock-skip-locked
// on the jobs table already coordinates multi-worker deploys when we
// need them; leader election is deferred until a concrete HA need
// lands (RD3).
//
// Phase 1 ships the skeleton: config validation, lifecycle wiring, the
// admin port, and placeholders for the scanner/processor loops. Later
// phases fill in the real logic without reshaping this seam.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/obs"
	"github.com/donaldgifford/rfc-api/internal/server"
)

// Deps are the dependencies Run consumes. Taken by pointer so the
// caller pays a pointer cost, not a 100+ byte struct copy.
type Deps struct {
	Config         config.Worker
	Registry       domain.DocumentTypeRegistry
	Pool           *pgxpool.Pool
	TracerProvider trace.TracerProvider
	Metrics        *obs.Metrics
	Logger         *slog.Logger
}

// Worker orchestrates the worker's loops. Construction is cheap; Run
// owns the actual lifecycle so the admin-port listener opens under a
// ctx the caller can cancel.
type Worker struct {
	cfg      config.Worker
	pool     *pgxpool.Pool
	tp       trace.TracerProvider
	metrics  *obs.Metrics
	logger   *slog.Logger
	ready    *readyState
	admin    *server.AdminServer
	sources  []config.SourceRepo
	disabled bool
}

// readyState is the watermark the /readyz probe reads. Atomic so the
// scanner goroutine can update without locking the probe.
type readyState struct {
	lastScanUnix atomic.Int64
}

// MarkScanned updates the watermark. Called by the scanner after a
// successful pass. Phase 4 wires this; Phase 1 leaves the call sites
// as TODO placeholders (marking on Run so /readyz flips to 200 once
// the worker is up).
func (r *readyState) MarkScanned(now time.Time) {
	r.lastScanUnix.Store(now.Unix())
}

// LastScan returns the most recent successful-scan timestamp, or
// zero time when no scan has completed yet.
func (r *readyState) LastScan() time.Time {
	u := r.lastScanUnix.Load()
	if u == 0 {
		return time.Time{}
	}
	return time.Unix(u, 0)
}

// New builds a Worker from Deps. Validates that every SourceRepo.TypeID
// maps to a registered type in the document-type registry so a
// misconfigured `work` pod fails fast rather than silently skipping a
// repo.
func New(deps *Deps) (*Worker, error) {
	if deps == nil {
		return nil, errors.New("worker: nil deps")
	}
	if deps.Registry == nil {
		return nil, errors.New("worker: nil registry")
	}
	// Validate config before the pool check so unit tests can exercise
	// source-repo validation without a real pool.
	if err := validateSources(deps.Config.SourceRepos, deps.Registry); err != nil {
		return nil, err
	}
	if deps.Pool == nil {
		return nil, errors.New("worker: nil pool")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	w := &Worker{
		cfg:      deps.Config,
		pool:     deps.Pool,
		tp:       deps.TracerProvider,
		metrics:  deps.Metrics,
		logger:   logger.With("component", "worker"),
		ready:    &readyState{},
		sources:  deps.Config.SourceRepos,
		disabled: len(deps.Config.SourceRepos) == 0,
	}
	w.admin = w.buildAdmin()
	return w, nil
}

// Run starts the admin port and sub-loops, blocking until ctx is
// canceled or any sub-loop returns a fatal error. The nil return on
// ctx cancel lets the CLI map a SIGTERM exit to code 0 cleanly.
func (w *Worker) Run(ctx context.Context) error {
	w.logSources(ctx)

	// Announce readiness immediately so kubelet's /readyz probe flips
	// to 200 once the worker has had a chance to load config. Phase 4
	// replaces this with an after-first-scan flip.
	w.ready.MarkScanned(time.Now())

	eg, egCtx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		if err := w.admin.Start(egCtx); err != nil {
			return fmt.Errorf("worker admin server: %w", err)
		}
		return nil
	})

	if !w.disabled {
		eg.Go(func() error { return w.runScanner(egCtx) })
		eg.Go(func() error { return w.runProcessor(egCtx) })
	}

	waitErr := eg.Wait()
	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		return waitErr
	}
	return ctx.Err()
}

// runScanner is the Phase-1 stub. Phase 4 fills in per-source
// enumeration + ingest-job enqueuing. Staying as a ctx-bound sleep
// loop here keeps the goroutine budget + lifecycle pipe observable.
func (w *Worker) runScanner(ctx context.Context) error {
	t := time.NewTicker(w.cfg.ScannerInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			w.logger.DebugContext(ctx, "scanner tick (stub)",
				"sources", len(w.sources))
			w.ready.MarkScanned(time.Now())
		}
	}
}

// runProcessor is the Phase-1 stub. Phase 3 wires the queue Lease
// loop; Phase 4 hooks the ingest handler. The current body polls at
// the configured interval and no-ops so operators can see the loop is
// alive in logs.
func (w *Worker) runProcessor(ctx context.Context) error {
	t := time.NewTicker(w.cfg.ProcessorPollInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			// No work queued yet; nothing to do. Drops back to the
			// ticker.
		}
	}
}

// buildAdmin composes the worker's admin-port HTTP server. Reuses
// server.NewAdmin so the worker exposes the same /healthz, /readyz,
// /metrics surface the API does. The readiness probe closes over
// w.ready + the pool so a DB outage flips /readyz to 503 within the
// poll cycle.
func (w *Worker) buildAdmin() *server.AdminServer {
	adminCfg := config.Admin{
		Listen:      w.cfg.AdminListen,
		ReadTimeout: 5 * time.Second,
	}
	probes := []server.ReadinessProbe{
		server.AlwaysReady{},
		poolProbe{pool: w.pool},
		scanProbe{ready: w.ready, interval: w.cfg.ScannerInterval, disabled: w.disabled},
	}
	return server.NewAdmin(adminCfg, probes, w.tp, w.metrics, w.logger)
}

// logSources emits one INFO line per configured source on start so
// operators can confirm the worker's live set without shelling into
// the pod.
func (w *Worker) logSources(ctx context.Context) {
	if w.disabled {
		w.logger.WarnContext(ctx, "worker started with no source_repos; idling",
			"admin_listen", w.cfg.AdminListen)
		return
	}
	w.logger.InfoContext(ctx, "worker started",
		"admin_listen", w.cfg.AdminListen,
		"sources", len(w.sources),
		"scanner_interval", w.cfg.ScannerInterval.String(),
		"processor_poll", w.cfg.ProcessorPollInterval.String(),
	)
	for _, s := range w.sources {
		w.logger.InfoContext(ctx, "source_repo",
			"type", s.TypeID,
			"repo", s.Repo,
			"path", s.Path,
			"branch", s.Branch,
			"parser", s.Parser,
		)
	}
}

// poolProbe surfaces database reachability on /readyz. Distinct from
// postgres.Probe (which lives in the store seam) because pulling in
// internal/store/postgres from here would create an import-graph tangle
// with the store depending on worker types later.
type poolProbe struct{ pool *pgxpool.Pool }

func (poolProbe) Name() string { return "postgres" }

func (p poolProbe) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := p.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	return nil
}

// scanProbe checks the scanner watermark. When the worker is idling
// (no sources) the probe always passes — an unconfigured worker is a
// warning, not a readiness failure. Otherwise the probe trips if the
// most recent scan is older than twice the configured interval.
type scanProbe struct {
	ready    *readyState
	interval time.Duration
	disabled bool
}

func (scanProbe) Name() string { return "scanner" }

func (s scanProbe) Check(context.Context) error {
	if s.disabled {
		return nil
	}
	last := s.ready.LastScan()
	if last.IsZero() {
		return errors.New("no scan yet")
	}
	if age := time.Since(last); age > 2*s.interval {
		return fmt.Errorf("last scan %s ago (interval=%s)", age.Round(time.Second), s.interval)
	}
	return nil
}

// validateSources verifies every SourceRepo.TypeID is known to the
// registry and that required fields aren't empty. Catches misconfig
// at startup (exit 1) rather than on the first scan.
func validateSources(sources []config.SourceRepo, reg domain.DocumentTypeRegistry) error {
	for i, s := range sources {
		if s.TypeID == "" {
			return fmt.Errorf("worker source_repos[%d]: type_id is required", i)
		}
		if _, ok := reg.Get(s.TypeID); !ok {
			return fmt.Errorf("worker source_repos[%d]: type_id %q not in registry", i, s.TypeID)
		}
		if s.Repo == "" {
			return fmt.Errorf("worker source_repos[%d]: repo is required", i)
		}
		if s.Path == "" {
			return fmt.Errorf("worker source_repos[%d]: path is required", i)
		}
	}
	return nil
}

// statically assert the probe types implement the interface.
var (
	_ server.ReadinessProbe = poolProbe{}
	_ server.ReadinessProbe = scanProbe{}
)
