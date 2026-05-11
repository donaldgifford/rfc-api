// Package slug implements GitHub-flavored heading slugification,
// faithful to upstream github-slugger / rehype-slug. See IMPL-0006
// for the rationale and the contract this enforces between rfc-api
// and any consumer that renders the same Markdown (notably rfc-site).
//
// Two surfaces are exported:
//
//   - Slug, a pure function that produces a slug for a single heading.
//   - Slugger, a stateful collision tracker that produces unique slugs
//     for a sequence of headings within one document (the H2 "Notes"
//     repeated three times becomes "notes", "notes-1", "notes-2").
//
// Algorithm: lowercase the input, strip every rune that is not a
// Unicode letter, digit, underscore, hyphen, or space, then replace
// each remaining space with a single hyphen. The input is not
// trimmed and runs of stripped runes are not collapsed; both
// behaviors mirror upstream github-slugger and intentionally diverge
// from the older rfc-api slugifier.
package slug

import (
	"fmt"
	"regexp"
	"strings"
)

// keepRune matches any rune that should be stripped from a heading
// before slug emission. The kept set is \p{L}\p{N}_- and the ASCII
// space (which is replaced with a hyphen in a separate pass).
var keepRune = regexp.MustCompile(`[^\p{L}\p{N}_\- ]`)

// Slug returns the heading slug for s. The function is pure and
// stateless; identical inputs always produce identical outputs.
//
// Callers that need collision suffixing across a sequence of
// headings within one document should use Slugger instead.
func Slug(s string) string {
	s = strings.ToLower(s)
	s = keepRune.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

// Slugger tracks slug occurrences within a single document so that
// duplicate headings collide deterministically into base, base-1,
// base-2, and so on. Behavior matches github-slugger's per-instance
// state: the first occurrence of a heading returns the bare slug,
// subsequent occurrences append "-N" where N is the next available
// index. A Slugger is not safe for concurrent use.
type Slugger struct {
	seen map[string]int
}

// NewSlugger returns a fresh Slugger with no recorded occurrences.
// Call once per document.
func NewSlugger() *Slugger {
	return &Slugger{seen: map[string]int{}}
}

// Slug returns the slug for s, suffixing with -N if the bare slug
// or a previous suffix has already been emitted by this Slugger.
// Suffixing is monotonic — if the caller manually feeds in a value
// equal to an existing suffix (e.g. Slug("Notes-1") after
// Slug("Notes")), the next Slug("Notes") returns "notes-2", not
// "notes-1".
func (g *Slugger) Slug(s string) string {
	base := Slug(s)
	result := base
	for {
		if _, exists := g.seen[result]; !exists {
			break
		}
		g.seen[base]++
		result = fmt.Sprintf("%s-%d", base, g.seen[base])
	}
	g.seen[result] = 0
	return result
}
