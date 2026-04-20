package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/obs"
	"github.com/donaldgifford/rfc-api/internal/server/httperr"
	"github.com/donaldgifford/rfc-api/internal/server/middleware"
)

// Deps are the dependencies needed to construct the main-port Server.
// Passed in at construction so the server itself has no global state
// and wiring stays visible at the cmd/ layer.
type Deps struct {
	Config         config.Server
	RateLimit      config.RateLimit
	Registry       domain.DocumentTypeRegistry
	Handlers       Handlers
	WebhookSecret  string
	TracerProvider trace.TracerProvider
	Metrics        *obs.Metrics
	Logger         *slog.Logger
}

// Server hosts user-facing HTTP traffic: /api/v1/* in Phase 2+,
// POST /api/v1/webhooks/github in Phase 2, catch-all 404 in Phase 1.
// Pair with an AdminServer on a separate port for ops + pprof.
type Server struct {
	http   *http.Server
	addr   string
	logger *slog.Logger
}

// New constructs the main-port server. Construction opens no sockets;
// Start(ctx) is responsible for binding the listener. This keeps the
// constructor pure and testable.
//
// When deps.Registry is nil the server falls back to a Phase 1-style
// catch-all 404 — useful for smoke-testing the lifecycle without a
// full registry. Production wiring must supply a registry and the
// handler set.
//
// Deps is taken by pointer so callers pay a pointer cost per call,
// not a 64+ byte copy.
func New(deps *Deps) *Server {
	var handler http.Handler
	if deps.Registry == nil {
		handler = fallbackMux()
	} else {
		v1 := V1ChainFromConfig(deps.Config, deps.RateLimit)
		handler = BuildMainHandler(
			deps.Handlers,
			deps.Registry,
			&v1,
			deps.WebhookSecret,
		)
	}

	chain := middleware.Chain(
		middleware.OTel(deps.TracerProvider),
		middleware.Recover,
		middleware.RequestID,
		middleware.Logger,
		middleware.Metrics(deps.Metrics),
	)

	return &Server{
		addr: deps.Config.Listen,
		http: &http.Server{
			Addr:              deps.Config.Listen,
			Handler:           chain(handler),
			ReadHeaderTimeout: deps.Config.ReadTimeout,
			ReadTimeout:       deps.Config.ReadTimeout,
			WriteTimeout:      deps.Config.WriteTimeout,
			IdleTimeout:       120 * time.Second,
		},
		logger: deps.Logger.With("component", "main-server"),
	}
}

// fallbackMux returns the Phase 1 catch-all used when no registry is
// configured. Kept as a named function for test reuse.
func fallbackMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httperr.Write(w, r, fmt.Errorf("no route for %s %s: %w",
			r.Method, r.URL.Path, domain.ErrNotFound))
	})
	return mux
}

// Start binds the listener and serves until ctx is canceled. Drains
// in-flight requests within the caller's ctx deadline on shutdown.
//
//   - nil on clean shutdown or http.ErrServerClosed.
//   - any other error returned up so the caller's errgroup sees it.
func (s *Server) Start(ctx context.Context) error {
	s.logger.InfoContext(ctx, "main server listening", "addr", s.addr)

	errCh := make(chan error, 1)
	go func() {
		err := s.http.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("main listen: %w", err)
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

func (s *Server) shutdown(ctx context.Context) error {
	deadline := 20 * time.Second
	if d, ok := ctx.Deadline(); ok {
		deadline = time.Until(d)
	}
	// Background parent on purpose; caller ctx is canceled -- see admin.go.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	s.logger.InfoContext(ctx, "main server draining", "budget", deadline)
	if err := s.http.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // background shutdownCtx intentional
		return fmt.Errorf("main shutdown: %w", err)
	}
	s.logger.InfoContext(ctx, "main server stopped")
	return nil
}
