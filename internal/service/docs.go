package service

import (
	"context"
	"fmt"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store"
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
func (d *Docs) ListByType(ctx context.Context, typeID string, limit int, cursor *store.Cursor) (store.Page, error) {
	if _, ok := d.registry.Get(typeID); !ok {
		return store.Page{}, fmt.Errorf("%w: unknown type %q", domain.ErrInvalidInput, typeID)
	}
	limit = normalizeLimit(limit)
	return d.store.List(ctx, store.ListQuery{TypeID: typeID, Limit: limit, Cursor: cursor})
}

// ListAll returns documents across all types, paginated.
func (d *Docs) ListAll(ctx context.Context, limit int, cursor *store.Cursor) (store.Page, error) {
	limit = normalizeLimit(limit)
	return d.store.List(ctx, store.ListQuery{Limit: limit, Cursor: cursor})
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
