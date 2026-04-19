package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
)

// runWork is the entry point for `rfc-api work`.
//
// v1 is a deliberate stub: logs a start line, blocks on ctx, logs a
// stop line on signal (see DESIGN-0001 Resolved Decision 6). Behaves
// like a real long-running daemon so Kubernetes doesn't crash-loop
// the pod and the lifecycle pipe (signal -> cancel -> clean exit) is
// observable before real worker logic lands in a follow-on IMPL.
func runWork(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("work", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse work flags: %w", err)
	}

	logger.InfoContext(ctx, "worker started (stub; no jobs processed yet)")
	<-ctx.Done()
	logger.InfoContext(ctx, "worker stopped")
	return ctx.Err()
}
