//go:build integration

// Package search_test exercises the real Meilisearch SDK path against
// a live server. The suite is gated with the `integration` build tag
// so `make test` stays dependency-free; CI runs it via
// `make test-integration-search` with MEILI_URL + MEILI_MASTER_KEY
// pointed at a service container.
package search_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
	meilisearchx "github.com/donaldgifford/rfc-api/internal/search/meilisearch"
)

const testIndex = "documents" // matches meilisearchx.IndexName

// meiliCfg returns the config block the tests drive Meili with, or
// skips the test when the dev creds aren't set. Sharing across tests
// in the binary means ApplySettings only runs once.
func meiliCfg(t *testing.T) config.Meili {
	t.Helper()
	url := os.Getenv("MEILI_URL")
	key := os.Getenv("MEILI_MASTER_KEY")
	if url == "" || key == "" {
		t.Skip("MEILI_URL / MEILI_MASTER_KEY not set; skipping integration test")
	}
	return config.Meili{URL: url, MasterKey: key}
}

// TestMain resets the `documents` index once per test binary run so
// tests can assert deterministic totals without serializing the
// suite. Each test that writes cleans up its own parent ids via
// t.Cleanup.
func TestMain(m *testing.M) {
	url := os.Getenv("MEILI_URL")
	key := os.Getenv("MEILI_MASTER_KEY")
	if url != "" && key != "" {
		if err := resetIndex(url, key); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}

func resetIndex(url, key string) error {
	c, err := meilisearchx.NewWriteClient(config.Meili{URL: url, MasterKey: key})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Delete + recreate the index so each binary run starts clean.
	// Ignore the "index does not exist" path — the subsequent create
	// handles both cases.
	task, err := c.Manager().DeleteIndexWithContext(ctx, testIndex)
	if err == nil {
		_, _ = c.Manager().WaitForTaskWithContext(ctx, task.TaskUID, 50*time.Millisecond)
	}
	return meilisearchx.ApplySettings(ctx, c)
}
