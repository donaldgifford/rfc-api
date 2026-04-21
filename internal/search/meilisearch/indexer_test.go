package meilisearch

import (
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
)

func TestBuildRecords_HeadPlusSections(t *testing.T) {
	dt := domain.DocumentType{ID: "rfc", Prefix: "RFC"}
	doc := &domain.Document{
		ID:        "RFC-0001",
		Type:      "rfc",
		Title:     "Example",
		Status:    "Accepted",
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
		Labels:    []string{"demo"},
		Authors:   []domain.Author{{Name: "Alice", Handle: "alice"}},
		Extensions: map[string]any{
			"Priority": "high",
		},
		Body: `Intro prose.

## First

first content.

## Second

second content.
`,
	}

	got := buildRecords(doc, dt)
	if len(got) != 3 {
		t.Fatalf("records = %d, want 3 (head + 2 H2s): %+v", len(got), got)
	}

	head := got[0]
	if head.ID != "RFC-0001" {
		t.Errorf("head id = %q, want RFC-0001", head.ID)
	}
	if head.ParentID != "RFC-0001" {
		t.Errorf("head parent_id = %q", head.ParentID)
	}
	if head.SectionHeading != "" {
		t.Errorf("head section_heading = %q, want empty", head.SectionHeading)
	}
	if head.Visibility != "internal" {
		t.Errorf("head visibility = %q, want internal", head.Visibility)
	}
	if len(head.AuthorHandles) != 1 || head.AuthorHandles[0] != "alice" {
		t.Errorf("author_handles = %v", head.AuthorHandles)
	}

	first := got[1]
	if first.ID != "RFC-0001#first" {
		t.Errorf("section 1 id = %q, want RFC-0001#first", first.ID)
	}
	if first.SectionHeading != "First" || first.SectionSlug != "first" {
		t.Errorf("section 1 heading/slug = %q/%q", first.SectionHeading, first.SectionSlug)
	}

	// Extensions flattened with type prefix.
	if got, ok := head.Extensions["ext_rfc_priority"]; !ok || got != "high" {
		t.Errorf("ext_rfc_priority = %v (%T), want high", got, got)
	}
}

func TestBuildRecords_FallsBackToNameForMissingHandle(t *testing.T) {
	dt := domain.DocumentType{ID: "rfc", Prefix: "RFC"}
	doc := &domain.Document{
		ID:      "RFC-0002",
		Type:    "rfc",
		Title:   "H",
		Authors: []domain.Author{{Name: "Carol"}},
		Body:    "## Only\nbody.",
	}
	got := buildRecords(doc, dt)
	if len(got) != 1 {
		t.Fatalf("want 1 record, got %d", len(got))
	}
	if got[0].AuthorHandles[0] != "Carol" {
		t.Errorf("author_handles = %v, want [Carol]", got[0].AuthorHandles)
	}
}

func TestBuildRecords_EmptyBody_SingleHead(t *testing.T) {
	dt := domain.DocumentType{ID: "adr", Prefix: "ADR"}
	doc := &domain.Document{
		ID:    "ADR-0001",
		Type:  "adr",
		Title: "x",
	}
	got := buildRecords(doc, dt)
	if len(got) != 1 {
		t.Fatalf("want 1 head record for empty body, got %d", len(got))
	}
	if got[0].ID != "ADR-0001" || got[0].BodyExcerpt != "" {
		t.Errorf("head = %+v", got[0])
	}
}

func TestIndexDocument_ToMap_FlattensExtensions(t *testing.T) {
	d := indexDocument{
		ID:         "RFC-0001",
		ParentID:   "RFC-0001",
		Type:       "rfc",
		Title:      "t",
		Visibility: "internal",
		Extensions: map[string]any{
			"ext_rfc_priority": "high",
			"ext_rfc_owner":    "alice",
		},
	}
	m := d.toMap()
	if got := m["ext_rfc_priority"]; got != "high" {
		t.Errorf("ext_rfc_priority = %v", got)
	}
	if _, ok := m["id"]; !ok {
		t.Errorf("id missing from flattened map")
	}
	// Section slug only set when non-empty.
	if _, ok := m["section_slug"]; ok {
		t.Errorf("section_slug should be omitted when empty")
	}
}

type mapTypeResolver map[string]domain.DocumentType

func (m mapTypeResolver) Get(id string) (domain.DocumentType, bool) {
	t, ok := m[id]
	return t, ok
}

func TestNewIndexer_RejectsNilDeps(t *testing.T) {
	if _, err := NewIndexer(nil, mapTypeResolver{}); err == nil {
		t.Error("want error for nil client")
	}
	// Fake client just to reach the types check.
	c := &Client{}
	if _, err := NewIndexer(c, nil); err == nil {
		t.Error("want error for nil types")
	}
}
