package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/obs"
	"github.com/donaldgifford/rfc-api/internal/search"
	meilisearchx "github.com/donaldgifford/rfc-api/internal/search/meilisearch"
	"github.com/donaldgifford/rfc-api/internal/server"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store/postgres"
	"github.com/donaldgifford/rfc-api/internal/worker/queue"
)

// runServe is the entry point for `rfc-api serve`.
//
// Loads config, sets up the OTel TracerProvider, constructs both the
// main-port and admin-port servers, runs them under an errgroup so
// either server's fatal error cancels the other, and drains both on
// signal-induced ctx cancellation within RFC_API_SHUTDOWN_TIMEOUT.
func runServe(ctx context.Context, logger *slog.Logger, args []string) error {
	cfg, err := config.Load(args, config.DefaultFilePath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Rebuild the logger now that we know the configured level and
	// format; install as slog default so every package that uses
	// slog.Default picks it up (middleware, readiness, httperr).
	logger = buildLogger(cfg.Log,
		slog.String("service", "rfc-api"),
		slog.String("version", version),
		slog.String("commit", commit),
	)
	slog.SetDefault(logger)

	tp, err := obs.NewTracerProvider(ctx, cfg.OTel, version, commit)
	if err != nil {
		return fmt.Errorf("build tracer provider: %w", err)
	}
	defer func() {
		// Shutdown with a short bounded budget so process exit isn't
		// blocked waiting for an unreachable collector.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := tp.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // background parent intentional
			logger.ErrorContext(ctx, "tracer provider shutdown", "err", err.Error())
		}
	}()

	// Open the database pool before constructing any server-layer
	// dependencies so a bad DATABASE_URL or unreachable DB fails fast
	// (exit code 1) rather than mid-request.
	pool, err := postgres.NewPool(ctx, cfg.Database.URL, logger)
	if err != nil {
		return fmt.Errorf("open postgres pool: %w", err)
	}
	defer pool.Close()

	reg, err := registry.New(cfg.DocumentTypes)
	if err != nil {
		return fmt.Errorf("build document-type registry: %w", err)
	}

	docsStore := postgres.NewDocs(pool)
	docsSvc := service.NewDocs(docsStore, reg)

	// Meili read client is scoped to search only. Construction fails
	// fast on a missing URL/key; runtime failures degrade /api/v1/search
	// to 503 while the rest of the API stays up.
	meiliClient, err := meilisearchx.NewReadClient(cfg.Meili)
	if err != nil {
		return fmt.Errorf("build meilisearch read client: %w", err)
	}
	logger.InfoContext(ctx, "meilisearch client configured", "url", meiliClient.URL())
	searchSvc := service.NewSearch(search.NoopClient{}, reg)
	handlers := server.Handlers{
		Docs:   handler.NewDocs(docsSvc),
		Search: handler.NewSearch(searchSvc),
		Types:  handler.NewTypes(reg),
		Webhook: handler.NewWebhook(&handler.WebhookConfig{
			Logger:  logger,
			Sources: cfg.Worker.SourceRepos,
			// API shares the jobs table with the worker — both
			// processes talk to the same Postgres; no in-process
			// channel between them.
			Queue: queue.New(pool, queue.Options{}),
		}),
	}

	probes := []server.ReadinessProbe{
		server.AlwaysReady{},
		postgres.Probe{Pool: pool},
		meilisearchx.Probe{Client: meiliClient},
	}
	metrics := obs.NewMetrics()

	admin := server.NewAdmin(cfg.Admin, probes, tp.Provider(), metrics, logger)
	main := server.New(&server.Deps{
		Config:         cfg.Server,
		RateLimit:      cfg.RateLimit,
		Registry:       reg,
		Handlers:       handlers,
		WebhookSecret:  cfg.Webhook.Secret,
		TracerProvider: tp.Provider(),
		Metrics:        metrics,
		Logger:         logger,
	})

	// Shared budget ctx derives from the signal-rooted ctx so a
	// SIGTERM cancels both. The errgroup bounds the shutdown so one
	// slow server can't hold the process up past the budget.
	eg, egCtx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		if err := main.Start(egCtx); err != nil {
			return fmt.Errorf("main server: %w", err)
		}
		return nil
	})
	eg.Go(func() error {
		if err := admin.Start(egCtx); err != nil {
			return fmt.Errorf("admin server: %w", err)
		}
		return nil
	})

	waitErr := eg.Wait()

	// A canceled context from SIGTERM is a graceful shutdown, not a
	// failure. Anything else surfaces to main() for exit-code mapping.
	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		// Detect a Shutdown-budget exhaustion and return the exit-code-2
		// sentinel so main() exits with the right code. For now, the
		// server shutdown paths don't surface a distinct deadline-
		// exceeded error; this hook is ready for that refinement.
		if errors.Is(waitErr, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", errShutdownTimedOut, waitErr)
		}
		return waitErr
	}

	return ctx.Err()
}
