package meilisearch

import (
	"encoding/json"
	"strings"
	"testing"

	meili "github.com/meilisearch/meilisearch-go"
)

func TestBuildFilter_DefaultIsVisibility(t *testing.T) {
	got := buildFilter("")
	if !strings.Contains(got, `visibility = "internal"`) {
		t.Errorf("filter = %q, want visibility constraint", got)
	}
	if strings.Contains(got, "type =") {
		t.Errorf("no type in query should not set type filter: %q", got)
	}
}

func TestBuildFilter_WithType(t *testing.T) {
	got := buildFilter("rfc")
	if !strings.Contains(got, `type = "rfc"`) {
		t.Errorf("filter = %q, missing type filter", got)
	}
	if !strings.Contains(got, "AND") {
		t.Errorf("filter = %q, expected AND between clauses", got)
	}
}

func TestNormalizeLimit(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 20}, {-5, 20}, {1, 1}, {99, 99}, {1000, 100},
	}
	for _, c := range cases {
		if got := normalizeLimit(c.in); got != c.want {
			t.Errorf("normalizeLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestCursorRoundtrip(t *testing.T) {
	for _, offset := range []int{0, 1, 20, 100, 999999} {
		c := encodeCursor(offset)
		got, err := decodeCursor(c)
		if err != nil {
			t.Errorf("decode(%q): %v", c, err)
		}
		if got != offset {
			t.Errorf("roundtrip = %d, want %d", got, offset)
		}
	}
}

func TestDecodeCursor_EmptyIsZero(t *testing.T) {
	off, err := decodeCursor("")
	if err != nil {
		t.Fatalf("empty cursor error = %v", err)
	}
	if off != 0 {
		t.Errorf("off = %d, want 0", off)
	}
}

func TestDecodeCursor_Garbage(t *testing.T) {
	if _, err := decodeCursor("!!!not-base64!!!"); err == nil {
		t.Error("want error on malformed cursor")
	}
}

func TestResultFromHit_PopulatesDocument(t *testing.T) {
	hit := meili.Hit{}
	mustSet := func(k string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		hit[k] = b
	}
	mustSet("id", "RFC-0001#first")
	mustSet("parent_id", "RFC-0001")
	mustSet("type", "rfc")
	mustSet("title", "Example")
	mustSet("section_heading", "First")
	mustSet("section_slug", "first")
	mustSet("body_excerpt", "excerpt content")
	mustSet("status", "Accepted")
	mustSet("labels", []string{"demo"})
	mustSet("author_handles", []string{"alice"})
	mustSet("created_at", int64(1704067200)) // 2024-01-01T00:00:00Z
	mustSet("updated_at", int64(1704153600))

	r, err := resultFromHit(hit)
	if err != nil {
		t.Fatalf("resultFromHit: %v", err)
	}
	if r.Document.ID != "RFC-0001" {
		t.Errorf("doc.ID = %q, want RFC-0001 (from parent_id)", r.Document.ID)
	}
	if r.Document.Title != "Example" {
		t.Errorf("doc.Title = %q", r.Document.Title)
	}
	if r.SectionHeading != "First" || r.SectionSlug != "first" {
		t.Errorf("section = %q/%q", r.SectionHeading, r.SectionSlug)
	}
	if r.Document.CreatedAt.IsZero() {
		t.Errorf("CreatedAt not hydrated")
	}
	if len(r.Document.Authors) != 1 || r.Document.Authors[0].Handle != "alice" {
		t.Errorf("authors = %+v", r.Document.Authors)
	}
}

func TestResultFromHit_SnippetsAndTerms(t *testing.T) {
	hit := meili.Hit{
		"id":              mustRaw(t, "RFC-0001"),
		"parent_id":       mustRaw(t, "RFC-0001"),
		"type":            mustRaw(t, "rfc"),
		"title":           mustRaw(t, "Example"),
		"body_excerpt":    mustRaw(t, "rate limit semantics"),
		"section_heading": mustRaw(t, "Rate limiting"),
		"_formatted": mustRaw(t, map[string]string{
			"body_excerpt":    "<em>rate</em> <em>limit</em> semantics",
			"title":           "Example",
			"section_heading": "<em>Rate</em> limiting",
		}),
	}
	r, err := resultFromHit(hit)
	if err != nil {
		t.Fatalf("resultFromHit: %v", err)
	}
	if !strings.Contains(r.Snippet, "<em>rate</em>") {
		t.Errorf("snippet missing <em> tag: %q", r.Snippet)
	}
	if len(r.MatchedTerms) != 2 {
		t.Errorf("matched_terms = %v, want [rate limit]", r.MatchedTerms)
	}
	if r.MatchedTerms[0] != "rate" || r.MatchedTerms[1] != "limit" {
		t.Errorf("matched_terms order wrong: %v", r.MatchedTerms)
	}
}

func TestResultFromHit_RejectsEmptyID(t *testing.T) {
	hit := meili.Hit{
		"parent_id": mustRaw(t, "x"),
		"type":      mustRaw(t, "rfc"),
		"title":     mustRaw(t, "x"),
	}
	if _, err := resultFromHit(hit); err == nil {
		t.Error("want error on empty id")
	}
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
