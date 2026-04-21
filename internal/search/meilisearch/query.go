package meilisearch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	meili "github.com/meilisearch/meilisearch-go"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/search"
)

// unixToTime converts a Unix epoch seconds value to a UTC time. Used
// to hydrate document timestamps out of index records.
func unixToTime(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

// highlightPreTag / highlightPostTag wrap matched terms in snippets.
// Keeping them simple `<em>` tags keeps the payload sanitizable on
// the HTML-rendering side — clients that don't render HTML read
// matched_terms instead.
const (
	highlightPreTag  = "<em>"
	highlightPostTag = "</em>"
)

// Query implements search.Client over Meilisearch. The receiver
// carries its own configured read-scoped Client so a handler can
// share the same connection pool across requests.
func (c *Client) Query(ctx context.Context, q search.Query) (search.Page, error) {
	offset, err := decodeCursor(q.Cursor)
	if err != nil {
		return search.Page{}, fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	limit := normalizeLimit(q.Limit)

	req := &meili.SearchRequest{
		Query:                 q.Q,
		Offset:                int64(offset),
		Limit:                 int64(limit),
		AttributesToHighlight: []string{"title", "body_excerpt", "section_heading"},
		HighlightPreTag:       highlightPreTag,
		HighlightPostTag:      highlightPostTag,
		ShowMatchesPosition:   true,
		Filter:                buildFilter(q.TypeID),
	}

	resp, err := c.svc.Index(IndexName).SearchWithContext(ctx, q.Q, req)
	if err != nil {
		return search.Page{}, fmt.Errorf("meilisearch search: %w", err)
	}

	hits := make([]search.Result, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		r, rerr := resultFromHit(h)
		if rerr != nil {
			return search.Page{}, rerr
		}
		hits = append(hits, r)
	}

	total := int(resp.EstimatedTotalHits)
	if resp.TotalHits > 0 {
		total = int(resp.TotalHits)
	}

	next := ""
	if len(hits) == limit && offset+limit < total {
		next = encodeCursor(offset + limit)
	}

	return search.Page{
		Hits:       hits,
		NextCursor: next,
		Total:      total,
	}, nil
}

// buildFilter returns a Meili filter expression. visibility is always
// constrained; the optional type filter ANDs in. Returning the string
// (not a slice-of-slices) keeps the filter shape recognizable in
// logs.
func buildFilter(typeID string) string {
	parts := []string{fmt.Sprintf(`visibility = %q`, visibilityInternal)}
	if typeID != "" {
		parts = append(parts, fmt.Sprintf(`type = %q`, typeID))
	}
	return strings.Join(parts, " AND ")
}

// normalizeLimit caps limit at a sane max and applies a default so
// clients that omit the param still get paged results.
func normalizeLimit(limit int) int {
	const (
		defaultLimit = 20
		maxLimit     = 100
	)
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	default:
		return limit
	}
}

// resultFromHit decodes a Meili hit into a search.Result with a
// domain.Document populated from the fields Meili stores. Full-doc
// fields (body, links, discussion) stay zero — clients resolve those
// through /api/v1/{type}/{id}.
func resultFromHit(h meili.Hit) (search.Result, error) {
	var raw indexHit
	if err := h.DecodeInto(&raw); err != nil {
		return search.Result{}, fmt.Errorf("decode hit: %w", err)
	}
	if raw.ID == "" {
		return search.Result{}, errors.New("decode hit: empty id")
	}

	doc := domain.Document{
		ID:     domain.DocumentID(raw.ParentID),
		Type:   raw.Type,
		Title:  raw.Title,
		Status: raw.Status,
		Labels: raw.Labels,
	}
	if raw.CreatedAt > 0 {
		doc.CreatedAt = unixToTime(raw.CreatedAt)
	}
	if raw.UpdatedAt > 0 {
		doc.UpdatedAt = unixToTime(raw.UpdatedAt)
	}
	for _, handle := range raw.AuthorHandles {
		doc.Authors = append(doc.Authors, domain.Author{Handle: handle})
	}

	snippet, terms := highlightsFromFormatted(h, raw.BodyExcerpt, raw.Title, raw.SectionHeading)

	return search.Result{
		Document:       doc,
		Snippet:        snippet,
		MatchedTerms:   terms,
		SectionHeading: raw.SectionHeading,
		SectionSlug:    raw.SectionSlug,
	}, nil
}

// indexHit decodes the stored fields the indexer writes. Mirrors the
// shape indexDocument.toMap produces.
type indexHit struct {
	ID             string   `json:"id"`
	ParentID       string   `json:"parent_id"`
	Type           string   `json:"type"`
	Title          string   `json:"title"`
	SectionHeading string   `json:"section_heading"`
	SectionSlug    string   `json:"section_slug"`
	BodyExcerpt    string   `json:"body_excerpt"`
	Status         string   `json:"status"`
	Labels         []string `json:"labels"`
	AuthorHandles  []string `json:"author_handles"`
	CreatedAt      int64    `json:"created_at"`
	UpdatedAt      int64    `json:"updated_at"`
}

// formatted is the nested `_formatted` field Meili returns when any
// AttributesToHighlight were requested. Fields carry the highlighted
// version of the original attribute value.
type formatted struct {
	Title          string `json:"title"`
	BodyExcerpt    string `json:"body_excerpt"`
	SectionHeading string `json:"section_heading"`
}

// highlightTagPattern matches any <em>TERM</em> run. Non-greedy; used
// to harvest matched_terms from the snippet.
var highlightTagPattern = regexp.MustCompile(`<em>([^<]+)</em>`)

// highlightsFromFormatted picks the best available snippet from Meili's
// `_formatted` object: body_excerpt wins, then title, then
// section_heading, then the raw excerpt as a last-ditch fallback.
// MatchedTerms is the dedup'd set of every <em>-tagged run across all
// highlighted attributes.
func highlightsFromFormatted(h meili.Hit, rawExcerpt, rawTitle, rawHeading string) (string, []string) {
	// A malformed _formatted block is not worth failing the whole
	// query over — the raw excerpt below is a clean fallback. Drop
	// the error.
	f, _ := decodeFormatted(h) //nolint:errcheck // fallback to raw excerpt on decode failure

	snippet := firstNonEmpty(f.BodyExcerpt, f.Title, f.SectionHeading, rawExcerpt, rawTitle, rawHeading)

	seen := make(map[string]bool)
	var terms []string
	for _, src := range []string{f.BodyExcerpt, f.Title, f.SectionHeading} {
		for _, m := range highlightTagPattern.FindAllStringSubmatch(src, -1) {
			term := strings.ToLower(strings.TrimSpace(m[1]))
			if term == "" || seen[term] {
				continue
			}
			seen[term] = true
			terms = append(terms, term)
		}
	}
	return snippet, terms
}

func decodeFormatted(h meili.Hit) (formatted, error) {
	raw, ok := h["_formatted"]
	if !ok {
		return formatted{}, nil
	}
	var f formatted
	if err := json.Unmarshal(raw, &f); err != nil {
		return formatted{}, fmt.Errorf("decode _formatted: %w", err)
	}
	return f, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// encodeCursor serializes a numeric offset as an opaque base64 token.
// Clients never parse this — they hand it back as `cursor=...`. Keeps
// pagination shape uniform with the keyset cursors on list endpoints
// per IMPL-0005 RD4.
func encodeCursor(offset int) string {
	raw := "off:" + strconv.Itoa(offset)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor reverses encodeCursor. Empty or malformed cursors map
// to offset 0 so an empty client-provided value is never an error.
func decodeCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("cursor: %w", err)
	}
	const prefix = "off:"
	if !strings.HasPrefix(string(b), prefix) {
		return 0, fmt.Errorf("cursor: invalid shape %q", b)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(b), prefix))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("cursor: invalid offset %q", b)
	}
	return n, nil
}
