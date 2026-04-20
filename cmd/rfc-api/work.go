package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/obs"
	"github.com/donaldgifford/rfc-api/internal/parser"
	_ "github.com/donaldgifford/rfc-api/internal/parser/doczmarkdown" // register docz-markdown
	"github.com/donaldgifford/rfc-api/internal/store/postgres"
	"github.com/donaldgifford/rfc-api/internal/worker"
)

// runWork is the entry point for `rfc-api work`. Loads config, opens
// the shared Postgres pool (read-write), builds a worker, and runs
// the scanner + processor loops until ctx is canceled.
//
// Exit codes mirror `serve`:
//   - 0 on graceful shutdown (SIGTERM)
//   - 1 on startup failure (bad config, can't open pool)
//   - 2 on shutdown-budget exhaustion (wired via errShutdownTimedOut)
func runWork(ctx context.Context, logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("work", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse work flags: %w", err)
	}

	cfg, err := config.Load(flags.Args(), config.DefaultFilePath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger = buildLogger(cfg.Log,
		slog.String("service", "rfc-api-worker"),
		slog.String("version", version),
		slog.String("commit", commit),
	)
	slog.SetDefault(logger)

	tp, err := obs.NewTracerProvider(ctx, cfg.OTel, version, commit)
	if err != nil {
		return fmt.Errorf("build tracer provider: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := tp.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // background parent intentional
			logger.ErrorContext(ctx, "tracer provider shutdown", "err", err.Error())
		}
	}()

	pool, err := postgres.NewPool(ctx, cfg.Database.URL, logger)
	if err != nil {
		return fmt.Errorf("open postgres pool: %w", err)
	}
	defer pool.Close()

	reg, err := registry.New(cfg.DocumentTypes)
	if err != nil {
		return fmt.Errorf("build document-type registry: %w", err)
	}

	w, err := worker.New(&worker.Deps{
		Config:         cfg.Worker,
		Registry:       reg,
		Pool:           pool,
		Store:          postgres.NewDocs(pool),
		Parsers:        parser.Default,
		TracerProvider: tp.Provider(),
		Metrics:        obs.NewMetrics(),
		Logger:         logger,
	})
	if err != nil {
		return fmt.Errorf("build worker: %w", err)
	}

	if err := w.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("worker: %w", err)
	}
	return ctx.Err()
}
