package meilisearch

import (
	"strings"
	"testing"
)

func TestSplitSections_HeadPlusH2s(t *testing.T) {
	body := `Intro prose before the first heading. Carries meaning for search.

## First

first body.

## Second

second body.

## Third

third body.
`
	got := splitSections(body)
	// Expect 4 sections: head + 3 H2s.
	if len(got) != 4 {
		t.Fatalf("got %d sections, want 4: %+v", len(got), got)
	}
	if got[0].Heading != "" {
		t.Errorf("head heading = %q, want empty", got[0].Heading)
	}
	if !strings.Contains(got[0].Body, "Intro prose") {
		t.Errorf("head body missing intro: %q", got[0].Body)
	}
	if got[1].Heading != "First" || got[1].Slug != "first" {
		t.Errorf("section 1 = %+v, want heading=First slug=first", got[1])
	}
	if got[3].Heading != "Third" || !strings.Contains(got[3].Body, "third body") {
		t.Errorf("section 3 = %+v", got[3])
	}
}

func TestSplitSections_NoLeadingProse_DropsEmptyHead(t *testing.T) {
	body := `## Only

prose.
`
	got := splitSections(body)
	if len(got) != 1 {
		t.Fatalf("want 1 section, got %d: %+v", len(got), got)
	}
	if got[0].Heading != "Only" {
		t.Errorf("heading = %q", got[0].Heading)
	}
}

func TestSplitSections_H3DoesNotSplit(t *testing.T) {
	body := `## Parent

some text.

### Child

more text.

## Sibling

sibling text.
`
	got := splitSections(body)
	if len(got) != 2 {
		t.Fatalf("want 2 H2 splits (H3 folded in), got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Body, "more text") {
		t.Errorf("parent section dropped H3 content: %q", got[0].Body)
	}
}

func TestSplitSections_H1Splits(t *testing.T) {
	body := `# One
first.

# Two
second.
`
	got := splitSections(body)
	if len(got) != 2 {
		t.Fatalf("want 2 sections, got %d", len(got))
	}
	if got[0].Slug != "one" || got[1].Slug != "two" {
		t.Errorf("slugs = %q / %q", got[0].Slug, got[1].Slug)
	}
}

// TestSplitSections_DuplicateH2s_GetCollisionSuffixes verifies that
// the per-document Slugger is instantiated *inside* splitSections so
// repeat H2s in one body emit base, base-1, base-2 in order. This is
// the end-to-end proof that Phase 2's wiring (one slug.NewSlugger()
// per call) actually fires — unit tests in internal/slug exercise
// the Slugger directly but cannot prove splitSections threads it
// correctly.
func TestSplitSections_DuplicateH2s_GetCollisionSuffixes(t *testing.T) {
	body := `## Notes

first.

## Notes

second.

## Notes

third.
`
	got := splitSections(body)
	if len(got) != 3 {
		t.Fatalf("want 3 sections, got %d: %+v", len(got), got)
	}
	want := []string{"notes", "notes-1", "notes-2"}
	for i, w := range want {
		if got[i].Slug != w {
			t.Errorf("section %d slug = %q, want %q", i, got[i].Slug, w)
		}
		if got[i].Heading != "Notes" {
			t.Errorf("section %d heading = %q, want %q (collision suffixing applies to slug only)",
				i, got[i].Heading, "Notes")
		}
	}
}

// TestSplitSections_RepeatedCalls_ProduceIdenticalSlugs pins the
// per-call slugger contract: calling splitSections twice on the same
// body must yield byte-identical slug sequences. A regression here
// would mean we accidentally promoted the Slugger to package state.
func TestSplitSections_RepeatedCalls_ProduceIdenticalSlugs(t *testing.T) {
	body := `## Notes

first.

## Notes

second.
`
	a := splitSections(body)
	b := splitSections(body)
	if len(a) != len(b) {
		t.Fatalf("len mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Slug != b[i].Slug {
			t.Errorf("section %d slug differs across calls: %q vs %q (slugger state must reset per call)",
				i, a[i].Slug, b[i].Slug)
		}
	}
}

func TestSplitSections_LongSectionTruncated(t *testing.T) {
	body := "## Title\n\n" + strings.Repeat("word ", 300) // ~1500 chars
	got := splitSections(body)
	if len(got) != 1 {
		t.Fatalf("want 1 section, got %d", len(got))
	}
	if len([]rune(got[0].Body)) > bodyExcerptLen+1 { // +1 for the ellipsis
		t.Errorf("body not truncated: %d runes", len([]rune(got[0].Body)))
	}
	if !strings.HasSuffix(got[0].Body, "…") {
		t.Errorf("truncated body should end with ellipsis: %q", got[0].Body[len(got[0].Body)-10:])
	}
}
