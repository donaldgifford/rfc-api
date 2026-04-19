package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
)

// runServe is the entry point for `rfc-api serve`.
//
// This is a placeholder: the real wiring (config load, both http.Server
// instances, errgroup-coordinated lifecycle) lands with the
// `internal/server` and `internal/config` packages in subsequent tasks
// (see IMPL-0001 Phase 1 "Server construction + lifecycle" block). For
// now it parses no flags, logs a startup line, and blocks on ctx so
// the dispatch + signal plumbing can be exercised end-to-end.
func runServe(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse serve flags: %w", err)
	}

	logger.InfoContext(ctx, "serve starting (stub; server packages not yet wired)")
	<-ctx.Done()
	logger.InfoContext(ctx, "serve stopped")
	return ctx.Err()
}
