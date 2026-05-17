// Package main is the rfc-api CLI entry point.
//
// Subcommands:
//
//	rfc-api serve   start the HTTP server (main + admin ports)
//	rfc-api work    start the sync worker (stub in v1)
//	rfc-api migrate apply pending database migrations and exit
//	rfc-api reindex enqueue a reindex job for every document and exit
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
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/donaldgifford/rfc-api/internal/config"
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
	case "migrate":
		err = runMigrate(ctx, logger, rest)
	case "reindex":
		err = runReindex(ctx, logger, rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printUsage()
		return exitStartupFailure
	}

	return exitCodeFor(err, logger)
}

// loadCmdConfig is the shared flag-parse + config-load boilerplate for
// subcommands whose only flag is `-c` (config file path). Subcommands
// with additional flags (e.g. reindex's --dry-run) build their own
// FlagSet rather than calling this.
//
// `name` is used as the FlagSet name and in the error wrappers so
// failure messages name the offending subcommand.
func loadCmdConfig(name string, args []string) (*config.Config, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	configPath := flags.String("c", "", "path to config.yaml (overrides $RFC_API_CONFIG and the default "+config.DefaultFilePath+")")
	if err := flags.Parse(args); err != nil {
		return nil, fmt.Errorf("parse %s flags: %w", name, err)
	}
	cfg, err := config.Load(flags.Args(), config.ResolveFilePath(*configPath))
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
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
  migrate   apply pending database migrations and exit
  reindex   enqueue a reindex job for every document and exit
  version   print version and commit
  help      show this message

Configuration is via env vars (RFC_API_* and upstream-standard names
like DATABASE_URL / MEILI_MASTER_KEY / OTEL_EXPORTER_OTLP_ENDPOINT)
and flags; see docs/design/0001-*.md #Configuration for the full
surface.
`
	fmt.Print(usage)
}
