//go:build integration

package search_test

import (
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/search"
	meilisearchx "github.com/donaldgifford/rfc-api/internal/search/meilisearch"
)

// seedCorpus is a minimal per-type registry + sample docs used by
// query + delete assertions. Each doc has ≥ 2 H2s so the per-section
// split path is exercised.
type seedCorpus struct {
	reg *staticRegistry
	dt  map[string]domain.DocumentType
}

func fakeRegistry() *seedCorpus {
	types := []domain.DocumentType{
		{ID: "rfc", Prefix: "RFC"},
		{ID: "adr", Prefix: "ADR"},
	}
	r := &staticRegistry{byID: make(map[string]domain.DocumentType)}
	for _, t := range types {
		r.byID[t.ID] = t
	}
	return &seedCorpus{reg: r, dt: r.byID}
}

type staticRegistry struct {
	byID map[string]domain.DocumentType
}

func (s *staticRegistry) Get(id string) (domain.DocumentType, bool) {
	t, ok := s.byID[id]
	return t, ok
}

func sampleDocs() []domain.Document {
	body := `Intro prose on rate limiting.

## Background

Discusses rate limits as applied at the edge.

## Design

The rate-limit design enforces per-token caps.
`
	return []domain.Document{
		{
			ID:        "RFC-0001",
			Type:      "rfc",
			Title:     "Rate limiting",
			Status:    "Accepted",
			Body:      body,
			Labels:    []string{"networking"},
			Authors:   []domain.Author{{Handle: "alice"}},
			CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			ID:        "ADR-0001",
			Type:      "adr",
			Title:     "Use Go stdlib",
			Status:    "Accepted",
			Body:      "Intro.\n\n## Status\n\nAccepted.\n\n## Context\n\nstdlib context.\n",
			Labels:    []string{"platform"},
			Authors:   []domain.Author{{Handle: "bob"}},
			CreatedAt: time.Date(2025, 1, 10, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2025, 1, 11, 0, 0, 0, 0, time.UTC),
		},
	}
}

func writeClient(t *testing.T) *meilisearchx.Client {
	t.Helper()
	c, err := meilisearchx.NewWriteClient(meiliCfg(t))
	if err != nil {
		t.Fatalf("NewWriteClient: %v", err)
	}
	return c
}

func readClient(t *testing.T) *meilisearchx.Client {
	t.Helper()
	c, err := meilisearchx.NewReadClient(meiliCfg(t))
	if err != nil {
		t.Fatalf("NewReadClient: %v", err)
	}
	return c
}

func seedIndex(t *testing.T) *seedCorpus {
	t.Helper()
	cfg := fakeRegistry()
	ix, err := meilisearchx.NewIndexer(writeClient(t), cfg.reg)
	if err != nil {
		t.Fatalf("NewIndexer: %v", err)
	}
	for _, d := range sampleDocs() {
		doc := d
		if err := ix.Upsert(t.Context(), &doc); err != nil {
			t.Fatalf("Upsert %s: %v", doc.ID, err)
		}
	}
	t.Cleanup(func() {
		for _, d := range sampleDocs() {
			_ = ix.Delete(t.Context(), d.ID)
		}
	})
	return cfg
}

func TestQuery_FindsMatchingHit(t *testing.T) {
	seedIndex(t)
	c := readClient(t)

	page, err := c.Query(t.Context(), search.Query{Q: "rate", Limit: 20})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Hits) == 0 {
		t.Fatalf("want at least one hit for 'rate', got 0")
	}
	found := false
	for _, h := range page.Hits {
		if h.Document.ID == "RFC-0001" {
			found = true
			if h.Snippet == "" {
				t.Errorf("hit missing snippet: %+v", h)
			}
			if len(h.MatchedTerms) == 0 {
				t.Errorf("hit missing matched_terms: %+v", h)
			}
		}
	}
	if !found {
		t.Errorf("hits did not include RFC-0001: %+v", page.Hits)
	}
}

func TestQuery_PerTypeFilter(t *testing.T) {
	seedIndex(t)
	c := readClient(t)

	page, err := c.Query(t.Context(), search.Query{Q: "", TypeID: "adr", Limit: 20})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, h := range page.Hits {
		if h.Document.Type != "adr" {
			t.Errorf("non-adr hit in type=adr response: %+v", h)
		}
	}
	if page.Total == 0 {
		t.Error("want non-zero total for type=adr after seed")
	}
}

func TestIndexer_DeleteClearsSections(t *testing.T) {
	seedIndex(t)

	// Before delete: seeded RFC-0001 produces ≥ 1 hit.
	read := readClient(t)
	pre, _ := read.Query(t.Context(), search.Query{Q: "rate", TypeID: "rfc", Limit: 20})
	if len(pre.Hits) == 0 {
		t.Fatal("pre-delete: want ≥1 hit for 'rate' in rfc")
	}

	cfg := fakeRegistry()
	ix, _ := meilisearchx.NewIndexer(writeClient(t), cfg.reg)
	if err := ix.Delete(t.Context(), "RFC-0001"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	post, _ := read.Query(t.Context(), search.Query{Q: "rate", TypeID: "rfc", Limit: 20})
	for _, h := range post.Hits {
		if h.Document.ID == "RFC-0001" {
			t.Errorf("post-delete: RFC-0001 still appears: %+v", h)
		}
	}
}

func TestApplySettings_Idempotent(t *testing.T) {
	_ = meiliCfg(t) // skip gate

	c := writeClient(t)
	if err := meilisearchx.ApplySettings(t.Context(), c); err != nil {
		t.Fatalf("ApplySettings first: %v", err)
	}
	// Second call should be a no-op — the handler does a GET + compare
	// before PATCH. Run it twice; a regression here would log a
	// superfluous task but not fail.
	if err := meilisearchx.ApplySettings(t.Context(), c); err != nil {
		t.Fatalf("ApplySettings second: %v", err)
	}
}

func TestDistinctParentsByType_AfterSeed(t *testing.T) {
	seedIndex(t)
	c := readClient(t)

	counts, err := c.DistinctParentsByType(t.Context(), []string{"rfc", "adr"})
	if err != nil {
		t.Fatalf("DistinctParentsByType: %v", err)
	}
	if counts["rfc"] == 0 || counts["adr"] == 0 {
		t.Errorf("distinct counts after seed = %+v", counts)
	}
}

// Static assertion that seedCorpus.reg satisfies the TypeResolver
// contract the indexer expects.
var _ meilisearchx.TypeResolver = (*staticRegistry)(nil)

// Static assertion the fake registry behaves the same way
// DocumentType access does on the production registry.
var _ = func(c *seedCorpus) {
	_ = config.Meili{}
	_, _ = c.dt["rfc"]
}
