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
	"github.com/donaldgifford/rfc-api/internal/server/httperr"
	"github.com/donaldgifford/rfc-api/internal/server/middleware"
)

// Deps are the dependencies needed to construct the main-port Server.
// Passed in at construction so the server itself has no global state
// and wiring stays visible at the cmd/ layer.
type Deps struct {
	Config         config.Server
	TracerProvider trace.TracerProvider
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
// Phase 1: the main mux has only a catch-all 404 handler that emits
// RFC 7807 via httperr. Phase 2 registers /api/v1/* routes via the
// registry-driven loop (see DESIGN-0001 §Route registration).
//
// Deps is taken by pointer so callers pay a pointer cost per call,
// not a 64+ byte copy.
func New(deps *Deps) *Server {
	mux := http.NewServeMux()

	// Catch-all: Go 1.22+ ServeMux treats "/" as a prefix match for
	// anything not otherwise registered. Phase 2 routes are more
	// specific and win; this is the fallback.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httperr.Write(w, r, fmt.Errorf("no route for %s %s: %w",
			r.Method, r.URL.Path, domain.ErrNotFound))
	})

	chain := middleware.Chain(
		middleware.OTel(deps.TracerProvider),
		middleware.Recover,
		middleware.RequestID,
		middleware.Logger,
	)

	return &Server{
		addr: deps.Config.Listen,
		http: &http.Server{
			Addr:              deps.Config.Listen,
			Handler:           chain(mux),
			ReadHeaderTimeout: deps.Config.ReadTimeout,
			ReadTimeout:       deps.Config.ReadTimeout,
			WriteTimeout:      deps.Config.WriteTimeout,
			IdleTimeout:       120 * time.Second,
		},
		logger: deps.Logger.With("component", "main-server"),
	}
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
