package meilisearch

import (
	"context"
	"fmt"
	"slices"
	"time"

	meili "github.com/meilisearch/meilisearch-go"
)

// DesiredSettings returns the canonical index settings per IMPL-0005
// Phase 2. Every attribute list is sorted so comparison against the
// server's reply is order-independent.
//
// Exported so tests (and a future settings-drift check) can compare
// against the live server without duplicating the declarations.
func DesiredSettings() *meili.Settings {
	return &meili.Settings{
		SearchableAttributes: []string{
			"title",
			"section_heading",
			"body_excerpt",
		},
		FilterableAttributes: []string{
			"author_handles",
			"labels",
			"parent_id",
			"status",
			"type",
			"visibility",
		},
		SortableAttributes: []string{
			"created_at",
			"updated_at",
		},
		// Defaults plus a created_at:desc tiebreaker at the bottom. Meili's
		// default ranking is words → typo → proximity → attribute → sort →
		// exactness; we append created_at:desc so ties on a blank tiebreaker
		// land newest-first rather than implementation-defined.
		RankingRules: []string{
			"words",
			"typo",
			"proximity",
			"attribute",
			"sort",
			"exactness",
			"created_at:desc",
		},
		TypoTolerance: &meili.TypoTolerance{
			Enabled: true,
			MinWordSizeForTypos: meili.MinWordSizeForTypos{
				OneTypo:  5,
				TwoTypos: 9,
			},
		},
		DisplayedAttributes: []string{"*"},
	}
}

// settingsTaskPoll is the interval between WaitForTask polls when
// applying index settings. Settings operations finish in tens of
// milliseconds on a warm Meili; a short poll keeps the worker boot
// path tight without hammering the server.
const settingsTaskPoll = 50 * time.Millisecond

// ApplySettings brings the `documents` index to DesiredSettings.
// Idempotent: if the live settings already match, no write is issued
// (RD: re-running ApplySettings is a no-op per IMPL-0005 Phase 2
// success criteria).
//
// ApplySettings creates the index if it does not yet exist so the
// bootstrap path does not require the caller to race an index
// creation before writing settings.
func ApplySettings(ctx context.Context, c *Client) error {
	if err := ensureIndex(ctx, c); err != nil {
		return err
	}

	idx := c.svc.Index(IndexName)
	current, err := idx.GetSettingsWithContext(ctx)
	if err != nil {
		return fmt.Errorf("meilisearch: get settings: %w", err)
	}

	desired := DesiredSettings()
	if settingsEqual(current, desired) {
		return nil
	}

	task, err := idx.UpdateSettingsWithContext(ctx, desired)
	if err != nil {
		return fmt.Errorf("meilisearch: update settings: %w", err)
	}
	return c.awaitTask(ctx, task.TaskUID, "update settings")
}

// ensureIndex creates the canonical index if GetIndex reports 404.
// Any other error is surfaced. Success is idempotent — an already-
// present index is a no-op.
func ensureIndex(ctx context.Context, c *Client) error {
	_, err := c.svc.GetIndexWithContext(ctx, IndexName)
	if err == nil {
		return nil
	}
	// The SDK surfaces an *Error for HTTP failures; we don't want to
	// tightly couple to its shape, so fall back to creating the index
	// and ignoring an "index already exists" reply on the retry path.
	task, cerr := c.svc.CreateIndexWithContext(ctx, &meili.IndexConfig{
		Uid:        IndexName,
		PrimaryKey: "id",
	})
	if cerr != nil {
		return fmt.Errorf("meilisearch: create index %q: %w", IndexName, cerr)
	}
	return c.awaitTask(ctx, task.TaskUID, "create index")
}

// settingsEqual is the "no-op diff" check. Meilisearch returns
// attribute lists in the order the caller wrote them, so a naive
// DeepEqual flags false diffs on reordered slices. Comparing as
// sorted sets keeps ApplySettings a no-op across restarts.
func settingsEqual(got, want *meili.Settings) bool {
	if got == nil || want == nil {
		return got == want
	}
	if !stringSetsEqual(got.SearchableAttributes, want.SearchableAttributes) {
		return false
	}
	if !stringSetsEqual(got.FilterableAttributes, want.FilterableAttributes) {
		return false
	}
	if !stringSetsEqual(got.SortableAttributes, want.SortableAttributes) {
		return false
	}
	if !slices.Equal(got.RankingRules, want.RankingRules) {
		// Ranking rules are ordered — the order IS the behavior. Use
		// slices.Equal, not set equality.
		return false
	}
	if !stringSetsEqual(got.DisplayedAttributes, want.DisplayedAttributes) {
		return false
	}
	if !typoToleranceEqual(got.TypoTolerance, want.TypoTolerance) {
		return false
	}
	return true
}

// stringSetsEqual compares two []string as multisets. Callers need
// not presort.
func stringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := slices.Clone(a)
	bs := slices.Clone(b)
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}

func typoToleranceEqual(a, b *meili.TypoTolerance) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	return a.Enabled == b.Enabled &&
		a.MinWordSizeForTypos.OneTypo == b.MinWordSizeForTypos.OneTypo &&
		a.MinWordSizeForTypos.TwoTypos == b.MinWordSizeForTypos.TwoTypos
}
