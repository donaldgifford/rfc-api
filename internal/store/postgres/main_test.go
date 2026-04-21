//go:build integration

package postgres_test

import (
	"errors"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"

	"github.com/donaldgifford/rfc-api/db"
)

// TestMain applies the embedded migrations to DATABASE_URL before
// running the store-level integration tests. Without this, a fresh
// CI Postgres container hits the truncate() helper before the schema
// exists and every test fails with "relation does not exist". The
// server-level suite (test/integration/postgres/) has its own
// equivalent — both need to be present because each package's
// TestMain runs in isolation.
func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		os.Exit(m.Run())
	}
	if err := migrateUp(dsn); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func migrateUp(dsn string) error {
	mig, err := db.NewMigrator(dsn)
	if err != nil {
		return err
	}
	defer func() { _, _ = mig.Close() }()

	if err := mig.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
