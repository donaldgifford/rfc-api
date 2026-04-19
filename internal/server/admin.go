package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/server/middleware"
)

// AdminServer hosts the operational endpoints: /healthz, /readyz,
// /metrics, and optionally /debug/pprof/*. Isolated on its own port
// (config.Admin.Listen) so it's never exposed through ingress and its
// short middleware chain (no timeout, no auth, no rate-limit) stays
// clean -- pprof CPU profile is long-running by design and Prometheus
// scrape is aggressive enough that rate-limiting it would be pathological.
type AdminServer struct {
	http   *http.Server
	addr   string
	logger *slog.Logger
}

// NewAdmin builds the admin-port server. Construction opens no sockets.
//
// probes are registered in the /readyz registry. tp drives OTel span
// creation for admin traffic (spans for kubelet probes + scrape are
// useful when debugging probe flakes). PprofEnabled gates whether
// /debug/pprof/* is registered at all -- when false, those paths 404
// as if the package weren't imported.
func NewAdmin(cfg config.Admin, probes []ReadinessProbe, tp trace.TracerProvider, logger *slog.Logger) *AdminServer {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthLive)
	mux.HandleFunc("GET /readyz", HealthReady(probes))
	mux.Handle("GET /metrics", promhttp.Handler())

	if cfg.PprofEnabled {
		registerPprof(mux)
	}

	chain := middleware.Chain(
		middleware.OTel(tp),
		middleware.Recover,
		middleware.RequestID,
		middleware.Logger,
	)

	return &AdminServer{
		addr: cfg.Listen,
		http: &http.Server{
			Addr:    cfg.Listen,
			Handler: chain(mux),
			// Short read timeout is fine -- probes + scrape are
			// tiny. NO write timeout: pprof CPU profile is a long-
			// running response (30+ seconds) and a write deadline
			// would terminate it mid-capture.
			ReadHeaderTimeout: cfg.ReadTimeout,
			ReadTimeout:       cfg.ReadTimeout,
			IdleTimeout:       120 * time.Second,
		},
		logger: logger.With("component", "admin-server"),
	}
}

// Start binds the listener and serves until ctx is canceled. On
// cancel, drains in-flight requests within a shutdown budget derived
// from ctx (or 20s if ctx has no deadline). Returns:
//
//   - nil on clean shutdown (after ctx cancel + graceful drain).
//   - http.ErrServerClosed is translated to nil (expected on Shutdown).
//   - any other error is returned for the caller's errgroup to surface.
func (s *AdminServer) Start(ctx context.Context) error {
	s.logger.InfoContext(ctx, "admin server listening", "addr", s.addr)

	errCh := make(chan error, 1)
	go func() {
		err := s.http.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("admin listen: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		return s.shutdown(ctx)
	case err := <-errCh:
		return err
	}
}

// shutdown drains in-flight requests. Honors the caller's ctx
// deadline, with a 20s fallback if the caller's ctx has none.
func (s *AdminServer) shutdown(ctx context.Context) error {
	deadline := 20 * time.Second
	if d, ok := ctx.Deadline(); ok {
		deadline = time.Until(d)
	}
	// context.Background() is intentional: the caller's ctx is already
	// canceled (that's what triggered shutdown), so deriving from it
	// would expire the shutdown ctx immediately. deadline is computed
	// above from the caller's ctx, so we still honor its budget.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	s.logger.InfoContext(ctx, "admin server draining", "budget", deadline)
	if err := s.http.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // background shutdownCtx intentional; caller ctx is canceled
		return fmt.Errorf("admin shutdown: %w", err)
	}
	s.logger.InfoContext(ctx, "admin server stopped")
	return nil
}

// registerPprof mounts the net/http/pprof handlers on mux. Extracted
// so the `net/http/pprof` import is tree-shaken when the flag is off
// (well, it's imported either way because it's a compile-time
// dependency; the point is runtime behavior -- paths only exist when
// PprofEnabled).
func registerPprof(mux *http.ServeMux) {
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	// pprof.Handler("<name>") covers heap, goroutine, allocs, etc.
	// via the Index; nothing else to register explicitly.
}
