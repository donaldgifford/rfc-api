package meilisearch

import (
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// bodyExcerptLen is the hard cap on indexed per-section prose per OQ6
// / RD6. 500 chars is enough context for highlights without bloating
// the index.
const bodyExcerptLen = 500

// Section is one heading-hierarchy slice of a document body. Section
// Slug is the kebab-cased heading text (stable, so re-indexing the
// same document produces the same sub-doc ids). Heading is "" for
// the prose before the first H1/H2 — the "head" sub-doc per
// ADR-0003 #Ingest.
type Section struct {
	Heading string
	Slug    string
	Body    string
}

// splitSections walks the Markdown AST and returns one Section per
// H1/H2 heading plus a leading head Section for content that precedes
// the first such heading.
//
// Sections below H2 do not split further (OQ2 / RD2): fragmenting on
// H3+ inflates the index without meaningfully sharper hits, and
// ADR-0003's Oxide prior art stops at H2 as well.
func splitSections(body string) []Section {
	src := []byte(body)
	md := goldmark.New()
	doc := md.Parser().Parse(text.NewReader(src))

	type marker struct {
		heading string
		slug    string
		// start/end bracket the heading node itself; the body of this
		// section is src[end : next.start] (trimmed).
		start, end int
	}

	markers := []marker{{heading: "", slug: "", start: 0, end: 0}}

	if doc != nil {
		//nolint:errcheck,gosec // walker callback never errors; ast.Walk has no other failure mode.
		ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			h, ok := n.(*ast.Heading)
			if !ok || h.Level > 2 {
				return ast.WalkContinue, nil
			}
			heading := headingText(h, src)
			slug := slugify(heading)
			start, end := nodeRange(h, src)
			markers = append(markers, marker{heading: heading, slug: slug, start: start, end: end})
			return ast.WalkContinue, nil
		})
	}

	// Close the last section to the end of source.
	sections := make([]Section, 0, len(markers))
	for i, m := range markers {
		var bodyStart, bodyEnd int
		bodyStart = m.end
		if i+1 < len(markers) {
			bodyEnd = markers[i+1].start
		} else {
			bodyEnd = len(src)
		}
		if bodyStart > bodyEnd {
			bodyStart = bodyEnd
		}
		sections = append(sections, Section{
			Heading: m.heading,
			Slug:    m.slug,
			Body:    truncateExcerpt(strings.TrimSpace(string(src[bodyStart:bodyEnd]))),
		})
	}

	// Drop empty head section if a document leads with a heading and
	// has no prose before it. Keeps "4 sections for 3 H2s" exact.
	if len(sections) > 1 && sections[0].Heading == "" && sections[0].Body == "" {
		sections = sections[1:]
	}
	return sections
}

// headingText returns the plain-text concatenation of a heading node's
// inline children. Drops embedded links / emphasis / code spans.
func headingText(h *ast.Heading, src []byte) string {
	var b strings.Builder
	for c := h.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			b.Write(t.Segment.Value(src))
			continue
		}
		// Non-text inlines: walk children for their text segments.
		for cc := c.FirstChild(); cc != nil; cc = cc.NextSibling() {
			if t, ok := cc.(*ast.Text); ok {
				b.Write(t.Segment.Value(src))
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// nodeRange returns a [start, end) byte range for a block node by
// collapsing its Lines into a single span. Heading nodes in goldmark
// expose one Line covering the heading line.
func nodeRange(n ast.Node, src []byte) (int, int) {
	lines := n.Lines()
	if lines == nil || lines.Len() == 0 {
		return 0, 0
	}
	first := lines.At(0)
	last := lines.At(lines.Len() - 1)
	start := first.Start
	end := last.Stop
	// Include the heading's leading `#` markers (goldmark's Line skips
	// the `## ` prefix) by rewinding to the line's beginning.
	for start > 0 && src[start-1] != '\n' {
		start--
	}
	// Advance past the trailing newline so body extraction starts on
	// the next line.
	if end < len(src) && src[end] == '\n' {
		end++
	}
	return start, end
}

// nonSlugRune strips anything that isn't a Unicode letter, digit, or
// underscore/dash and collapses whitespace into single dashes. Matches
// GitHub's anchor generation closely enough for doc-facing ids.
var nonSlugRune = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlugRune.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// truncateExcerpt caps body to bodyExcerptLen runes. Truncation is on
// a rune boundary to avoid producing an invalid UTF-8 tail.
func truncateExcerpt(body string) string {
	if len(body) <= bodyExcerptLen {
		return body
	}
	runes := []rune(body)
	if len(runes) <= bodyExcerptLen {
		return body
	}
	// Back up to the nearest whitespace so the cut doesn't land
	// mid-word — preserves highlight readability.
	cut := bodyExcerptLen
	for cut > 0 && !isSpace(runes[cut]) {
		cut--
	}
	if cut == 0 {
		cut = bodyExcerptLen
	}
	return strings.TrimRight(string(runes[:cut]), " \n\r\t") + "…"
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\n' || r == '\r' || r == '\t'
}
