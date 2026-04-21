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

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Open Questions", "open-questions"},
		{"Phase 1: Client, config, and key separation", "phase-1-client-config-and-key-separation"},
		{"  Already slug  ", "already-slug"},
		{"Non-ASCII: café ☕", "non-ascii-caf"},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
