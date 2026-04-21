package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	meilisearchx "github.com/donaldgifford/rfc-api/internal/search/meilisearch"
	"github.com/donaldgifford/rfc-api/internal/store/postgres"
	"github.com/donaldgifford/rfc-api/internal/worker/queue"
	"github.com/donaldgifford/rfc-api/internal/worker/reindex"
)

// runReindex is the entry point for `rfc-api reindex`.
//
// Enumerates every document id from Postgres and enqueues one
// `reindex` job per document. The running worker drains them through
// the Phase 3 indexer, rebuilding the search index in place. Dedup
// key `doc:<id>` collapses repeated invocations to one in-flight job
// per document — running `make reindex` twice while the first pass
// is still draining is a no-op, not a duplicate.
//
// Flag:
//
//	--dry-run   print the work set + exit without enqueuing anything
func runReindex(ctx context.Context, _ *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("reindex", flag.ContinueOnError)
	dryRun := flags.Bool("dry-run", false, "print document ids and exit without enqueuing")
	checkDrift := flags.Bool("check-drift", false, "compare Postgres vs Meili per-type and exit without enqueuing")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse reindex flags: %w", err)
	}

	cfg, err := config.Load(flags.Args(), config.DefaultFilePath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := buildLogger(cfg.Log,
		slog.String("service", "rfc-api-reindex"),
		slog.String("version", version),
		slog.String("commit", commit),
	)
	slog.SetDefault(logger)

	pool, err := postgres.NewPool(ctx, cfg.Database.URL, logger)
	if err != nil {
		return fmt.Errorf("open postgres pool: %w", err)
	}
	defer pool.Close()

	store := postgres.NewDocs(pool)

	if *checkDrift {
		return printDriftReport(ctx, cfg, store, logger)
	}

	ids, err := store.AllIDs(ctx)
	if err != nil {
		return fmt.Errorf("enumerate documents: %w", err)
	}
	logger.InfoContext(ctx, "reindex plan",
		"documents", len(ids), "dry_run", *dryRun)

	if *dryRun {
		for _, id := range ids {
			fmt.Println(id)
		}
		return nil
	}

	q := queue.New(pool, queue.Options{})
	var enqueued, skipped int
	for _, id := range ids {
		payload := reindex.Payload{DocumentID: string(id)}
		if err := q.Enqueue(ctx,
			reindex.KindReindex,
			"doc:"+string(id),
			payload,
			time.Time{},
		); err != nil {
			// The queue collapses duplicates via (kind, dedup_key); a
			// conflict means a reindex is already in flight, not a
			// failure. Keep counting and drive on.
			logger.WarnContext(ctx, "enqueue skipped",
				"document_id", id, "err", err.Error())
			skipped++
			continue
		}
		enqueued++
	}
	logger.InfoContext(ctx, "reindex enqueued",
		"documents", len(ids), "enqueued", enqueued, "skipped", skipped)
	return nil
}

// printDriftReport hits Postgres and Meili for per-type counts and
// prints a table to stdout. Non-zero deltas exit 1 so a scheduled
// `make reindex-check-drift` can gate on "drift = 0" cleanly in CI
// or an ops wrapper script.
func printDriftReport(ctx context.Context, cfg *config.Config, store *postgres.Docs, logger *slog.Logger) error {
	pgCounts, err := store.CountByType(ctx)
	if err != nil {
		return fmt.Errorf("postgres count: %w", err)
	}

	reg, err := registry.New(cfg.DocumentTypes)
	if err != nil {
		return fmt.Errorf("build registry: %w", err)
	}
	types := make([]string, 0, len(reg.List()))
	for _, t := range reg.List() {
		types = append(types, t.ID)
	}

	meiliClient, err := meilisearchx.NewReadClient(cfg.Meili)
	if err != nil {
		return fmt.Errorf("build meilisearch client: %w", err)
	}
	meiliCounts, err := meiliClient.DistinctParentsByType(ctx, types)
	if err != nil {
		return fmt.Errorf("meilisearch count: %w", err)
	}

	report := meilisearchx.CompareDrift(pgCounts, meiliCounts)
	fmt.Printf("%-12s %10s %10s %10s\n", "type", "postgres", "meili", "delta")
	fmt.Printf("%-12s %10s %10s %10s\n", "----", "--------", "-----", "-----")
	var maxAbs int
	for _, r := range report {
		fmt.Printf("%-12s %10d %10d %+10d\n", r.Type, r.Postgres, r.Meili, r.Delta)
		if d := abs(r.Delta); d > maxAbs {
			maxAbs = d
		}
	}
	if maxAbs > 0 {
		logger.WarnContext(ctx, "search drift detected",
			"max_abs_delta", maxAbs)
		return fmt.Errorf("drift detected (max abs delta %d) -- run `make reindex`", maxAbs)
	}
	logger.InfoContext(ctx, "search drift clean")
	return nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
