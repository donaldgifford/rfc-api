// Package list defines the cross-cutting types the cross-type list
// endpoint (/api/v1/docs) hands between the HTTP seam and the store.
// Phase 1 (IMPL-0007) ships only the Sort enum + parser; Phase 2 grows
// the package to host the variadic functional options the store
// consumes (see DESIGN-0003 #Sort-semantics and IMPL-0007 OQ3).
//
// list never imports net/http, database/sql, or any HTTP framework —
// it is pure types so handlers, stores, and the (Phase 4) contract
// test can all reach the same names without cross-package gymnastics.
package list

import (
	"errors"
	"fmt"
)

// Sort names a documented sort key for listDocs. The set is closed:
// adding a new value is a deliberate spec change with a matching enum
// extension in api/openapi.yaml. Values are lowercase snake_case to
// match the on-the-wire shape clients submit via ?sort=… parameter.
type Sort string

// The Sort enum. SortCreatedDesc is the default when ?sort= is absent
// (DESIGN-0003 #OQ3): matches today's `ORDER BY created_at DESC, id ASC`
// keyset, so existing callers see no change.
const (
	SortCreatedDesc Sort = "created_desc"
	SortCreatedAsc  Sort = "created_asc"
	SortUpdatedDesc Sort = "updated_desc"
	SortUpdatedAsc  Sort = "updated_asc"
	SortIDDesc      Sort = "id_desc"
	SortIDAsc       Sort = "id_asc"
)

// DefaultSort is the value used when a caller submits no ?sort= param.
// Pinned to SortCreatedDesc per DESIGN-0003 #OQ3 so unfiltered callers
// continue to receive the same ordering they get today.
const DefaultSort = SortCreatedDesc

// ErrInvalidSort is returned by ParseSort for any value outside the
// closed enum. Callers wrap this with domain.ErrInvalidInput at the
// HTTP edge to map to a 400 problem+json (IMPL-0007 #OQ2).
var ErrInvalidSort = errors.New("list: invalid sort")

// ParseSort canonicalizes the on-the-wire ?sort= value. An empty
// string returns DefaultSort with no error so handlers can pass
// r.URL.Query().Get("sort") in directly.
func ParseSort(s string) (Sort, error) {
	if s == "" {
		return DefaultSort, nil
	}
	switch Sort(s) {
	case SortCreatedDesc, SortCreatedAsc,
		SortUpdatedDesc, SortUpdatedAsc,
		SortIDDesc, SortIDAsc:
		return Sort(s), nil
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidSort, s)
}

// Valid reports whether s is a recognized Sort value. Cheap predicate
// for store-layer dispatch tables that want to assert preconditions
// without re-parsing.
func (s Sort) Valid() bool {
	switch s {
	case SortCreatedDesc, SortCreatedAsc,
		SortUpdatedDesc, SortUpdatedAsc,
		SortIDDesc, SortIDAsc:
		return true
	}
	return false
}
