// Package store defines the persistence seam between service code
// and concrete backends (in-memory for unit tests, Postgres for
// production). Service code depends on these interfaces; concrete
// stores live in sub-packages and never import the service layer.
//
// The interfaces return framework-agnostic domain types and the
// sentinel domain errors (domain.ErrNotFound, etc.). HTTP translation
// happens at the handler seam, not here.
package store

import (
	"context"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store/list"
)

// Page is the list-endpoint return shape. Items is already sorted;
// NextCursor is nil when the result exhausts the data set. Total is
// the filtered count of matching documents (used by X-Total-Count).
// Callers that need the *unfiltered* total — for example, the
// X-Total-Count-Unfiltered header when a filter is active per
// DESIGN-0003 #Total-count-headers — use Docs.CountAll separately.
type Page struct {
	Items      []domain.Document
	NextCursor *list.Cursor
	Total      int
}

// Docs is the set of document operations exposed by a store. Reads
// are the entire v1 surface. All methods honor ctx for cancellation
// and tracing.
//
// List takes a variadic list.Option set (IMPL-0007 #OQ3). The empty
// option set is equivalent to today's "all docs, default sort, default
// limit, no cursor" query so callers can extend their option list
// additively as new filter fields land without breaking call sites
// already in the tree.
type Docs interface {
	Get(ctx context.Context, id domain.DocumentID) (domain.Document, error)
	List(ctx context.Context, opts ...list.Option) (Page, error)
	CountAll(ctx context.Context) (int, error)
	Links(ctx context.Context, id domain.DocumentID) ([]domain.Link, error)
	Discussion(ctx context.Context, id domain.DocumentID) (domain.Discussion, error)
	Authors(ctx context.Context, id domain.DocumentID) ([]domain.Author, error)
	Revisions(ctx context.Context, id domain.DocumentID) ([]Revision, error)

	// Upsert inserts or replaces a document. The doc pointer avoids a
	// 264-byte pass-by-value on hot loops.
	Upsert(ctx context.Context, doc *domain.Document) error
}

// Revision is a stub in Phase 2 (see IMPL-0001). Populated by the
// worker and stored by the real Postgres store in a later phase.
type Revision struct {
	Commit    string            `json:"commit"`
	Message   string            `json:"message"`
	Author    domain.Author     `json:"author"`
	CreatedAt time.Time         `json:"created_at"`
	ID        domain.DocumentID `json:"document_id"`
}
