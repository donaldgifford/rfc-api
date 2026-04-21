package db

import (
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // driver registration
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// NewMigrator builds a golang-migrate instance sourced from the
// embedded SQL files and targeting databaseURL. Callers own the
// returned *migrate.Migrate and must Close it on shutdown.
//
// Factored out of cmd/rfc-api/migrate.go so the CLI entry and the
// integration tests in test/integration/postgres share one path.
func NewMigrator(databaseURL string) (*migrate.Migrate, error) {
	source, err := iofs.New(Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return m, nil
}
