// Package store defines the persistence seam between service code
// and concrete backends (in-memory for Phase 2, Postgres for Phase
// 3+). Service code depends on these interfaces; concrete stores
// live in sub-packages and never import the service layer.
//
// The interfaces return framework-agnostic domain types and the
// sentinel domain errors (domain.ErrNotFound, etc.). HTTP translation
// happens at the handler seam, not here.
package store

import (
	"context"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
)

// ListQuery names the parameters a list-shaped store call accepts.
// TypeID empty means cross-type (used by /api/v1/docs); non-empty
// narrows to one registered type.
//
// Cursor is opaque at this layer: the service encodes it to/from
// the wire and hands the decoded tuple in on the Cursor field.
type ListQuery struct {
	TypeID string
	Limit  int
	Cursor *Cursor
}

// Cursor is the decoded pagination cursor. Documents are sorted
// (CreatedAt DESC, ID ASC) per DESIGN-0001 #API surface; the cursor
// captures the last row on the previous page so the store can
// continue from the right position without offsets.
type Cursor struct {
	CreatedAt time.Time
	ID        domain.DocumentID
}

// Page is the list-endpoint return shape. Items is already sorted;
// NextCursor is nil when the result exhausts the data set. Total is
// the unfiltered count of matching documents (for X-Total-Count).
type Page struct {
	Items      []domain.Document
	NextCursor *Cursor
	Total      int
}

// Docs is the set of document operations exposed by a store. Reads
// are the entire v1 surface; Upsert is a stub in IMPL-0002 (real
// write semantics arrive with IMPL-0003's worker). All methods honor
// ctx for cancellation and tracing.
type Docs interface {
	Get(ctx context.Context, id domain.DocumentID) (domain.Document, error)
	List(ctx context.Context, q ListQuery) (Page, error)
	Links(ctx context.Context, id domain.DocumentID) ([]domain.Link, error)
	Discussion(ctx context.Context, id domain.DocumentID) (domain.Discussion, error)
	Authors(ctx context.Context, id domain.DocumentID) ([]domain.Author, error)
	Revisions(ctx context.Context, id domain.DocumentID) ([]Revision, error)

	// Upsert inserts or replaces a document. Stubbed in IMPL-0002 —
	// the in-memory store and the Postgres store both return a
	// not-implemented error. IMPL-0003 wires the real path; the
	// doc pointer avoids a 264-byte pass-by-value on hot loops.
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
