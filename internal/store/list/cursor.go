package list

import (
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
)

// Cursor is the decoded pagination cursor. The cursor captures the
// last row on the previous page so a store can continue from the
// right position without offsets.
//
// Sort names the ordering the cursor was minted under. The handler
// rejects cross-sort cursor reuse with 400 per DESIGN-0003
// #Error-contract; the cursor package surfaces the cursor's sort via
// the Sort field so the handler can compare it against the request's
// active sort before the store sees the call.
//
// SortValue holds the sort-column value for time-based sorts
// (created_*, updated_*). For id-based sorts it is the zero time and
// the keyset comparison runs on ID alone — the store dispatches on
// Sort and selects the appropriate column.
//
// Cursor moved here from internal/store in IMPL-0007 Phase 2 so the
// list-options package can reference it without creating a circular
// import (the options' WithCursor constructor needs to accept a
// *Cursor by name).
type Cursor struct {
	Sort      Sort
	SortValue time.Time
	ID        domain.DocumentID
}
