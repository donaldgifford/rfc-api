package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/donaldgifford/rfc-api/db"
	"github.com/donaldgifford/rfc-api/internal/config"
)

// runMigrate applies all pending SQL migrations against the database
// identified by DATABASE_URL, then exits. The CLI accepts no subcommand
// in v1 — it always runs up-to-latest. Rollback is an operator-only
// path and not surfaced here; operators with a reason to roll back run
// `mise exec -- migrate -database ... -path db/migrations down` directly
// (see IMPL-0002 Phase 1 RD6: explicit migration execution).
//
// Exit semantics mirror the rest of the CLI: a nil return leaves main
// at exitOK; any error propagates through exitCodeFor to exit 1.
func runMigrate(ctx context.Context, logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	cfg, err := config.Load(flags.Args(), config.DefaultFilePath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	m, err := newMigrator(cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("construct migrator: %w", err)
	}
	defer closeMigrator(m, logger)

	logger.InfoContext(ctx, "applying migrations", "database_url_host", maskedHost(cfg.Database.URL))

	switch err := m.Up(); {
	case err == nil:
		logger.InfoContext(ctx, "migrations applied")
	case errors.Is(err, migrate.ErrNoChange):
		logger.InfoContext(ctx, "migrations already up to date")
	default:
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}

// newMigrator builds a Migrate instance using the embedded SQL files
// as its source and DATABASE_URL as its target. The iofs source is
// safe for concurrent Up/Down calls — not that we need that, but the
// cost of using it is zero.
func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	source, err := iofs.New(migrationsFS(), "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return m, nil
}

// migrationsFS is a narrow wrapper for unit-test substitution. In
// production it hands back the embed.FS that ships with the binary.
func migrationsFS() fs.FS { return db.Migrations }

// closeMigrator is a defer-safe shutdown that logs both the source
// and database close errors. golang-migrate returns two errors so we
// can't wrap with %w cleanly; the context is logger-only.
func closeMigrator(m *migrate.Migrate, logger *slog.Logger) {
	srcErr, dbErr := m.Close()
	if srcErr != nil {
		logger.Warn("close migration source", "err", srcErr.Error())
	}
	if dbErr != nil {
		logger.Warn("close database", "err", dbErr.Error())
	}
}

// maskedHost returns a best-effort host fragment of the DATABASE_URL
// for log lines. We intentionally never log the full URL because it
// carries the password in query form.
func maskedHost(databaseURL string) string {
	// Defensive: anything off-shape returns an empty string. Operators
	// have other signals (the config-load log line) if the URL is bad.
	for i := 0; i < len(databaseURL)-2; i++ {
		if databaseURL[i] == '@' {
			for j := i + 1; j < len(databaseURL); j++ {
				if databaseURL[j] == '/' || databaseURL[j] == '?' {
					return databaseURL[i+1 : j]
				}
			}
			return databaseURL[i+1:]
		}
	}
	return ""
}
