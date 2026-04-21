// Package search defines the search client seam. The v1 client is a
// no-op that returns an empty page; a Meilisearch-backed client lands
// in Phase 3. Service code depends on the interface, not on a
// concrete client.
package search

import (
	"context"

	"github.com/donaldgifford/rfc-api/internal/domain"
)

// Query is the decoded search request the service passes down.
type Query struct {
	Q      string
	TypeID string
	Limit  int
	Cursor string
}

// Result is one hit returned to the handler. Score is backend-defined
// and opaque to the caller; the handler passes it through on the
// wire so clients can debug ranking without the server understanding
// the ranking model.
//
// Snippet is an HTML-tagged excerpt (<em>term</em>) for HTML-rendering
// clients; MatchedTerms is the same hit's terms as bare strings for
// clients that don't render HTML (MCP tool, CLI) per IMPL-0005 RD7.
// SectionHeading + SectionSlug surface the per-section shape so the
// frontend can deep-link into a heading.
type Result struct {
	Document       domain.Document `json:"document"`
	Score          float64         `json:"score,omitempty"`
	Snippet        string          `json:"snippet,omitempty"`
	MatchedTerms   []string        `json:"matched_terms,omitempty"`
	SectionHeading string          `json:"section_heading,omitempty"`
	SectionSlug    string          `json:"section_slug,omitempty"`
}

// Page is the paginated hit set. NextCursor is the backend's next-
// page token; opaque to callers. Total is the total estimated hit
// count (Meilisearch returns an estimate, not an exact count).
type Page struct {
	Hits       []Result
	NextCursor string
	Total      int
}

// Client is the persistence-agnostic search contract.
type Client interface {
	Query(ctx context.Context, q Query) (Page, error)
}

// NoopClient always returns an empty page. Used in Phase 2 before
// Meilisearch wiring lands so the handler surface can be exercised
// end-to-end without a running search backend.
type NoopClient struct{}

// Query returns an empty page and nil error.
func (NoopClient) Query(context.Context, Query) (Page, error) {
	return Page{}, nil
}
