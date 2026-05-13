package list

// Option configures a list-query Config. Stores accept a variadic
// `opts ...Option` argument and assemble the Config internally before
// dispatching to the right query path. IMPL-0007 #OQ3 picked this
// shape over a struct-arg signature so future filter fields can land
// additively without churning every call site.
//
// An Option is a function; package-internal Config is mutated in
// place. Constructors live alongside this file (WithSort, WithTypes,
// WithLimit, WithCursor); stores call Apply to materialize a
// validated Config.
type Option func(*Config)

// Config holds the assembled state of a list call. The zero value is
// a valid "unfiltered, default sort, no cursor, default limit"
// query — matching the semantics today's callers get when passing
// store.ListQuery{}.
//
// Limit is intentionally unbounded at this layer; the service is the
// seam that clamps it to DefaultListLimit / MaxListLimit. Stores
// reject Limit <= 0 with domain.ErrInvalidInput as a defense in depth.
type Config struct {
	Sort    Sort
	TypeIDs []string
	Limit   int
	Cursor  *Cursor
}

// Apply builds a Config from a slice of options. Returns the
// finished struct by value (small; the gocritic hugeParam threshold
// is 80 bytes and Config is well under that on amd64). Used by store
// implementations as the first line of their List method:
//
//	cfg := list.Apply(opts...)
//	if cfg.Limit <= 0 { ... }
//
// Apply also normalizes a zero Sort to DefaultSort so downstream
// dispatch can switch on the enum without a nil-or-default branch
// at every call site.
func Apply(opts ...Option) Config {
	var c Config
	for _, opt := range opts {
		opt(&c)
	}
	if c.Sort == "" {
		c.Sort = DefaultSort
	}
	return c
}

// WithSort sets the active sort. Empty / zero sort falls back to
// DefaultSort during Apply so callers can pass list.Sort("") as a
// no-op.
func WithSort(s Sort) Option {
	return func(c *Config) {
		c.Sort = s
	}
}

// WithTypes scopes the result set to one or more registered document
// type ids. An empty slice or zero-arg call is a no-op (cross-type).
// Multiple values are OR-semantics within the field per DESIGN-0003
// #Filter-semantics; AND across fields will arrive with future
// filter fields.
func WithTypes(ids ...string) Option {
	return func(c *Config) {
		if len(ids) == 0 {
			return
		}
		c.TypeIDs = append(c.TypeIDs, ids...)
	}
}

// WithLimit sets the page size. A non-positive value is left
// unchanged so the store-level guard catches it consistently.
func WithLimit(n int) Option {
	return func(c *Config) {
		c.Limit = n
	}
}

// WithCursor sets the pagination cursor. Nil is the explicit "first
// page" value; passing a non-nil cursor whose Sort disagrees with
// the Config's Sort is the handler's responsibility to detect — the
// store treats Config.Sort as authoritative.
func WithCursor(cur *Cursor) Option {
	return func(c *Config) {
		c.Cursor = cur
	}
}
