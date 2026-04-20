// Package main is the rfc-api CLI entry point.
//
// Subcommands:
//
//	rfc-api serve   start the HTTP server (main + admin ports)
//	rfc-api work    start the sync worker (stub in v1)
//	rfc-api version print version and commit
//	rfc-api help    show usage
//
// Exit codes (see DESIGN-0001 #Server lifecycle):
//
//	0  graceful shutdown or successful command completion
//	1  startup failure (listen error, unknown subcommand, parse error)
//	2  shutdown exceeded RFC_API_SHUTDOWN_TIMEOUT
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// Populated at build time via -ldflags (see Makefile).
var (
	version = "dev"
	commit  = "unknown"
)

// Exit codes. main() is the only place os.Exit is called.
const (
	exitOK               = 0
	exitStartupFailure   = 1
	exitShutdownTimedOut = 2
)

// errShutdownTimedOut is returned by subcommand runners when graceful
// shutdown exceeds its budget.
var errShutdownTimedOut = errors.New("shutdown exceeded budget")

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the testable core of main(). Returns the process exit code.
func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return exitOK
	}

	switch args[0] {
	case "help", "-h", "--help":
		printUsage()
		return exitOK
	case "version", "-v", "--version":
		fmt.Printf("rfc-api %s (%s)\n", version, commit)
		return exitOK
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT, syscall.SIGTERM,
	)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger = logger.With(
		"service", "rfc-api",
		"version", version,
		"commit", commit,
	)

	cmd, rest := args[0], args[1:]

	var err error
	switch cmd {
	case "serve":
		err = runServe(ctx, logger, rest)
	case "work":
		err = runWork(ctx, logger, rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printUsage()
		return exitStartupFailure
	}

	return exitCodeFor(err, logger)
}

// exitCodeFor maps a subcommand's returned error to a process exit code.
// A canceled context (signal-induced shutdown) is a graceful exit.
func exitCodeFor(err error, logger *slog.Logger) int {
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return exitOK
	case errors.Is(err, errShutdownTimedOut):
		logger.Error("shutdown timed out", "err", err)
		return exitShutdownTimedOut
	default:
		logger.Error("startup or runtime failure", "err", err)
		return exitStartupFailure
	}
}

func printUsage() {
	const usage = `rfc-api -- Markdown Portal backend

Usage:
  rfc-api <command> [flags]

Commands:
  serve     start the HTTP server (main + admin ports)
  work      start the sync worker (stub; logs and blocks on ctx in v1)
  version   print version and commit
  help      show this message

Configuration is via env vars (RFC_API_* and upstream-standard names
like DATABASE_URL / MEILI_MASTER_KEY / OTEL_EXPORTER_OTLP_ENDPOINT)
and flags; see docs/design/0001-*.md #Configuration for the full
surface.
`
	fmt.Print(usage)
}
