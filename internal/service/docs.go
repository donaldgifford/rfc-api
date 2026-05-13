package service

import (
	"context"
	"fmt"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store"
	"github.com/donaldgifford/rfc-api/internal/store/list"
)

// MaxListLimit is the hard cap on list-endpoint page size per
// DESIGN-0001 #API surface. A client-requested larger limit collapses
// to this; a zero or negative value is rejected as invalid input.
const MaxListLimit = 200

// DefaultListLimit is the fallback page size when the client supplies
// no `limit` query parameter.
const DefaultListLimit = 50

// Docs is the read-side document service. It knows about pagination
// bounds and the registry's set of known types; it does not speak
// HTTP, does not parse cursors (that's the handler / render seam),
// and does not format error envelopes.
type Docs struct {
	store    store.Docs
	registry domain.DocumentTypeRegistry
}

// NewDocs constructs a Docs service.
func NewDocs(s store.Docs, r domain.DocumentTypeRegistry) *Docs {
	return &Docs{store: s, registry: r}
}

// Get returns the document with id. The id is already in canonical
// display form (`RFC-0001`) when it reaches the service; conversion
// from the URL form happens at the handler via docid.Canonical.
func (d *Docs) Get(ctx context.Context, id domain.DocumentID) (domain.Document, error) {
	return d.store.Get(ctx, id)
}

// ListByType returns documents of the given type, paginated.
// Returns domain.ErrInvalidInput when typeID is not registered.
func (d *Docs) ListByType(ctx context.Context, typeID string, limit int, cursor *list.Cursor) (store.Page, error) {
	if _, ok := d.registry.Get(typeID); !ok {
		return store.Page{}, fmt.Errorf("%w: unknown type %q", domain.ErrInvalidInput, typeID)
	}
	return d.store.List(
		ctx,
		list.WithTypes(typeID),
		list.WithLimit(normalizeLimit(limit)),
		list.WithCursor(cursor),
	)
}

// ListAll returns documents across all types, paginated. typeIDs
// filters the result set to the OR-union of the given types (the
// caller is responsible for validating each id against the registry);
// an empty / nil slice is unfiltered. sort selects the ordering; the
// zero value is treated as list.DefaultSort.
//
// The handler is the single source of validation for typeIDs + sort
// — by the time the request reaches the service, every value here
// has already been gated through parseListAllQuery.
func (d *Docs) ListAll(
	ctx context.Context,
	limit int,
	cursor *list.Cursor,
	typeIDs []string,
	sort list.Sort,
) (store.Page, error) {
	opts := []list.Option{
		list.WithLimit(normalizeLimit(limit)),
		list.WithCursor(cursor),
	}
	if len(typeIDs) > 0 {
		opts = append(opts, list.WithTypes(typeIDs...))
	}
	if sort != "" {
		opts = append(opts, list.WithSort(sort))
	}
	return d.store.List(ctx, opts...)
}

// CountAll returns the unfiltered document count. Used by the
// handler to populate X-Total-Count-Unfiltered when a filter is
// active (DESIGN-0003 #Total-count-headers).
func (d *Docs) CountAll(ctx context.Context) (int, error) {
	return d.store.CountAll(ctx)
}

// Links returns both outgoing and incoming references for id.
func (d *Docs) Links(ctx context.Context, id domain.DocumentID) ([]domain.Link, error) {
	return d.store.Links(ctx, id)
}

// Discussion returns the discussion summary for id.
func (d *Docs) Discussion(ctx context.Context, id domain.DocumentID) (domain.Discussion, error) {
	return d.store.Discussion(ctx, id)
}

// Authors returns the authors recorded for id.
func (d *Docs) Authors(ctx context.Context, id domain.DocumentID) ([]domain.Author, error) {
	return d.store.Authors(ctx, id)
}

// Revisions returns the revision history for id. Stub in Phase 2.
func (d *Docs) Revisions(ctx context.Context, id domain.DocumentID) ([]store.Revision, error) {
	return d.store.Revisions(ctx, id)
}

// normalizeLimit clamps a client-supplied limit into the [1,
// MaxListLimit] range, defaulting empty / zero to DefaultListLimit.
// Negative values collapse to DefaultListLimit rather than erroring
// so the handler surface can stay ergonomic; explicit validation of
// an out-of-range client value happens at the handler before it
// reaches here.
func normalizeLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultListLimit
	case limit > MaxListLimit:
		return MaxListLimit
	default:
		return limit
	}
}
