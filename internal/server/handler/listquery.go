package handler

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/server/cursor"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store/list"
)

// listquery.go holds the pure parsers for the listDocs ?filter= and
// ?sort= query parameters introduced in DESIGN-0003 / IMPL-0007.
//
// The types here are unexported by design (IMPL-0007 #OQ1): the
// contract test asserts behavior at the HTTP layer through
// BuildMainHandler, not at the type-construction layer, so there is no
// reason to widen the package's API surface.

// filter is the parsed form of a single ?filter=field:value pair.
// Field and Value are validated against shape constraints in
// parseFilters but the value's semantic validity (e.g. "is this a
// known type id?") is the handler's job — that check needs the live
// document-type registry which this file does not import.
type filter struct {
	Field string
	Value string
}

// filterFieldPattern matches the field side of `field:value`. Lowercase
// snake_case, starting with a letter, no surrounding whitespace —
// matches the OpenAPI ListDocsFilter parameter pattern.
var filterFieldPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// filterValuePattern matches the value side. Permissive enough for
// the Phase 1 type-id values (`^[a-z][a-z0-9-]*$`) without
// pre-rejecting future fields with broader value charsets. Strict
// enough to block stray colons, whitespace, and non-ASCII bytes that
// could surprise the store-layer ANY() predicate.
var filterValuePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// errBadFilter is the package-local sentinel for any malformed
// ?filter= value. Wrapped with domain.ErrInvalidInput at the handler
// edge per IMPL-0007 #OQ2.
var errBadFilter = errors.New("bad filter")

// errBadSort is the package-local sentinel for any malformed ?sort=
// value, mirroring errBadFilter. Wraps list.ErrInvalidSort with a
// handler-friendlier message; the underlying chain is preserved so
// httperr.classify still maps it to 400.
var errBadSort = errors.New("bad sort")

// errCursorSortMismatch is the package-local sentinel for the
// cross-check between an inbound ?cursor= and ?sort=: a cursor minted
// under one sort cannot be honored under a different sort. Phase 3
// wraps this with domain.ErrInvalidInput at the handler edge so the
// client sees 400 + problem+json (DESIGN-0003 #Error-contract).
var errCursorSortMismatch = errors.New("cursor sort mismatch")

// parseFilters turns the raw r.URL.Query()["filter"] slice into a
// validated []filter. Each value must take the form `field:value`
// with no leading/trailing whitespace and exactly one ASCII colon.
// Empty input returns (nil, nil) so unfiltered callers stay untouched.
//
// parseFilters does not look up the semantic validity of the value
// (e.g. "is this a known type id?"). That check belongs to the
// handler, which has the document-type registry on hand.
func parseFilters(raw []string) ([]filter, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]filter, 0, len(raw))
	for i, v := range raw {
		f, err := parseFilter(v)
		if err != nil {
			return nil, fmt.Errorf("%w: index %d: %w", errBadFilter, i, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// parseFilter is the per-value helper. Split out so error messages
// can name the offending row index without rebuilding the formatting
// in every branch.
func parseFilter(s string) (filter, error) {
	if s == "" {
		return filter{}, errors.New("empty")
	}
	field, value, ok := splitOnce(s, ':')
	if !ok {
		return filter{}, fmt.Errorf("missing ':' separator in %q", s)
	}
	if field == "" {
		return filter{}, fmt.Errorf("empty field in %q", s)
	}
	if value == "" {
		return filter{}, fmt.Errorf("empty value in %q", s)
	}
	if !filterFieldPattern.MatchString(field) {
		return filter{}, fmt.Errorf("field %q does not match %s", field, filterFieldPattern)
	}
	if !filterValuePattern.MatchString(value) {
		return filter{}, fmt.Errorf("value %q does not match %s", value, filterValuePattern)
	}
	return filter{Field: field, Value: value}, nil
}

// splitOnce splits s at the first occurrence of sep. Returns
// (before, after, true) when sep is present, ("","",false) otherwise.
// Refuses inputs containing more than one separator so the
// `field:value` shape stays unambiguous (a stray `version:1.2`
// would otherwise silently parse as field=version, value=1.2 — but
// the value regex rejects the `.` anyway, surfaced via the regex
// branch in parseFilter).
func splitOnce(s string, sep byte) (string, string, bool) {
	idx := -1
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			if idx >= 0 {
				return "", "", false // multiple separators → caller rejects
			}
			idx = i
		}
	}
	if idx < 0 {
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}

// parseSort wraps list.ParseSort with the handler-local sentinel so
// all four query-string failure modes (bad filter, bad sort, bad
// cursor, cursor sort mismatch) share a uniform error envelope at
// the handler edge.
func parseSort(raw string) (list.Sort, error) {
	s, err := list.ParseSort(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errBadSort, err)
	}
	return s, nil
}

// listAllQuery captures the full parsed-and-validated query string for
// GET /api/v1/docs (the cross-type list endpoint). Limit / cursor are
// the historical pagination shape; typeIDs / sort are the IMPL-0007
// additions. HasFilter tells the handler whether to emit
// X-Total-Count-Unfiltered (DESIGN-0003 #Total-count-headers).
type listAllQuery struct {
	Limit     int
	Cursor    *list.Cursor
	TypeIDs   []string
	Sort      list.Sort
	HasFilter bool
}

// parseListAllQuery owns the full parse + validate sequence for the
// cross-type list endpoint. Order matters: limit and cursor come
// first so today's-shape callers still see today's error if their
// known params are bad, then filter / sort layer on top.
//
// Filter values are validated against the registry — unknown type
// ids return a 400 with the offending id named in the detail
// message. The cursor-sort cross-check is the last gate so the more
// specific "your cursor was minted under sort X" error wins over a
// stale-but-otherwise-correct request.
func parseListAllQuery(r *http.Request, reg domain.DocumentTypeRegistry) (listAllQuery, error) {
	q := r.URL.Query()
	out := listAllQuery{}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > service.MaxListLimit {
			return out, fmt.Errorf("%w: limit must be 1..%d", domain.ErrInvalidInput, service.MaxListLimit)
		}
		out.Limit = n
	}

	cur, err := cursor.Decode(q.Get("cursor"))
	if err != nil {
		return out, fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	out.Cursor = cur

	filters, err := parseFilters(q["filter"])
	if err != nil {
		return out, fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	for _, f := range filters {
		if f.Field != "type" {
			return out, fmt.Errorf(
				"%w: filter field %q not supported (phase 1: only 'type')",
				domain.ErrInvalidInput, f.Field,
			)
		}
		if _, ok := reg.Get(f.Value); !ok {
			return out, fmt.Errorf("%w: unknown type %q", domain.ErrInvalidInput, f.Value)
		}
		out.TypeIDs = append(out.TypeIDs, f.Value)
	}
	out.HasFilter = len(filters) > 0

	sort, err := parseSort(q.Get("sort"))
	if err != nil {
		return out, fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	out.Sort = sort

	// Cursor-sort cross-check: a cursor minted under one sort cannot
	// be honored under another. Skip when no cursor is present (an
	// unsorted-cursor request is the most common shape).
	if cur != nil && cur.Sort != "" && cur.Sort != sort {
		return out, fmt.Errorf(
			"%w: %w: cursor=%s, request=%s",
			domain.ErrInvalidInput, errCursorSortMismatch, cur.Sort, sort,
		)
	}

	return out, nil
}
