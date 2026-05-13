// Package memory is an in-process store.Docs used as a test fake.
// Production code uses internal/store/postgres exclusively after
// IMPL-0002 Phase 5; this package remains so server-, handler-, and
// service-layer unit tests can exercise the full stack without a
// database running.
//
// The store is read-only after construction and safe for concurrent
// reads. Tests that need write semantics use Add before the server
// starts.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store"
	"github.com/donaldgifford/rfc-api/internal/store/list"
)

// Store is an in-memory store.Docs.
type Store struct {
	byID    map[domain.DocumentID]domain.Document
	ordered []domain.Document // sorted by (CreatedAt DESC, ID ASC)
	links   map[domain.DocumentID][]domain.Link
}

// New builds an empty store.
func New() *Store {
	return &Store{
		byID:  make(map[domain.DocumentID]domain.Document),
		links: make(map[domain.DocumentID][]domain.Link),
	}
}

// LoadDir reads every *.json file under dir (non-recursive) into the
// store. Each file is one domain.Document in the wire-format shape.
// Returns a wrapped error naming the offending file on parse failure
// so seed typos surface with a useful message at startup.
func LoadDir(fsys fs.FS, dir string) (*Store, error) {
	s := New()
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read seed dir %q: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.ToSlash(filepath.Join(dir, e.Name()))
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", path, err)
		}
		var doc domain.Document
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse %q: %w", path, err)
		}
		if err := s.add(&doc); err != nil {
			return nil, fmt.Errorf("add %q: %w", path, err)
		}
	}
	s.sortIndex()
	return s, nil
}

// Add inserts a document into the store; useful in tests and the
// registry "fake type" check. Returns an error if the document id
// already exists — Phase 2 seed data must not collide.
func (s *Store) Add(doc *domain.Document) error {
	if err := s.add(doc); err != nil {
		return err
	}
	s.sortIndex()
	return nil
}

func (s *Store) add(doc *domain.Document) error {
	if doc.ID == "" {
		return errors.New("document id is required")
	}
	if _, dup := s.byID[doc.ID]; dup {
		return fmt.Errorf("duplicate document id %q", doc.ID)
	}
	s.byID[doc.ID] = *doc
	s.links[doc.ID] = append([]domain.Link(nil), doc.Links...)
	return nil
}

func (s *Store) sortIndex() {
	ordered := make([]domain.Document, 0, len(s.byID))
	for id := range s.byID {
		ordered = append(ordered, s.byID[id])
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].CreatedAt.After(ordered[j].CreatedAt)
		}
		return ordered[i].ID < ordered[j].ID
	})
	s.ordered = ordered
}

// Get returns the document with id, or domain.ErrNotFound.
func (s *Store) Get(_ context.Context, id domain.DocumentID) (domain.Document, error) {
	doc, ok := s.byID[id]
	if !ok {
		return domain.Document{}, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}
	return doc, nil
}

// List applies the assembled options against the in-memory index.
// Filtering, sorting, and cursor seek are all done in Go: copy the
// document slice, apply the type filter, sort by the active list.Sort,
// seek past the cursor, then slice to limit. ~50 lines because the
// in-memory store must reach full parity with Postgres so unit and
// contract tests can run Postgres-free (IMPL-0007 #OQ8).
func (s *Store) List(_ context.Context, opts ...list.Option) (store.Page, error) {
	cfg := list.Apply(opts...)
	if cfg.Limit <= 0 {
		return store.Page{}, fmt.Errorf("%w: limit must be positive", domain.ErrInvalidInput)
	}

	rows := s.filter(cfg.TypeIDs)
	total := len(rows)

	sortRows(rows, cfg.Sort)
	rows = seekPastCursor(rows, cfg.Sort, cfg.Cursor)

	page := store.Page{Total: total}
	if len(rows) > cfg.Limit {
		last := rows[cfg.Limit-1]
		page.NextCursor = nextCursor(&last, cfg.Sort)
		page.Items = append(page.Items, rows[:cfg.Limit]...)
	} else {
		page.Items = append(page.Items, rows...)
	}
	return page, nil
}

// CountAll returns the unfiltered document count — used by the
// handler to populate X-Total-Count-Unfiltered when a filter is
// active (DESIGN-0003 #Total-count-headers; IMPL-0007 #OQ5).
func (s *Store) CountAll(_ context.Context) (int, error) {
	return len(s.ordered), nil
}

// filter copies s.ordered and applies the type-id OR-filter. An
// empty TypeIDs slice returns a full copy. The copy is mandatory:
// sortRows mutates its argument and List runs concurrently with
// itself in tests.
func (s *Store) filter(typeIDs []string) []domain.Document {
	out := make([]domain.Document, 0, len(s.ordered))
	for i := range s.ordered {
		if len(typeIDs) > 0 && !slices.Contains(typeIDs, s.ordered[i].Type) {
			continue
		}
		out = append(out, s.ordered[i])
	}
	return out
}

// sortRows orders rows in place per the active sort. The comparator
// always uses ID as a deterministic tiebreaker so equal sort-column
// values produce a stable order across calls.
func sortRows(rows []domain.Document, s list.Sort) {
	sort.Slice(rows, func(i, j int) bool {
		return less(&rows[i], &rows[j], s)
	})
}

func less(a, b *domain.Document, s list.Sort) bool {
	switch s {
	case list.SortCreatedDesc:
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID < b.ID
	case list.SortCreatedAsc:
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	case list.SortUpdatedDesc:
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	case list.SortUpdatedAsc:
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.Before(b.UpdatedAt)
		}
		return a.ID < b.ID
	case list.SortIDDesc:
		return a.ID > b.ID
	case list.SortIDAsc:
		return a.ID < b.ID
	}
	// Defensive default: same as SortCreatedDesc — never reached
	// because list.Apply normalizes the zero value to DefaultSort.
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID < b.ID
}

// seekPastCursor returns the slice tail starting after the row the
// cursor names. Sort-aware: time-based sorts compare on CreatedAt /
// UpdatedAt with the SortValue field; id-based sorts use ID alone.
func seekPastCursor(rows []domain.Document, s list.Sort, cur *list.Cursor) []domain.Document {
	if cur == nil {
		return rows
	}
	idx := sort.Search(len(rows), func(i int) bool {
		return rowAfterCursor(&rows[i], s, cur)
	})
	return rows[idx:]
}

// rowAfterCursor reports whether row r sorts strictly after the
// cursor under sort s. Used as the `f` predicate for sort.Search,
// which returns the first index where this is true.
func rowAfterCursor(r *domain.Document, s list.Sort, cur *list.Cursor) bool {
	switch s {
	case list.SortCreatedDesc:
		if !r.CreatedAt.Equal(cur.SortValue) {
			return r.CreatedAt.Before(cur.SortValue)
		}
		return r.ID > cur.ID
	case list.SortCreatedAsc:
		if !r.CreatedAt.Equal(cur.SortValue) {
			return r.CreatedAt.After(cur.SortValue)
		}
		return r.ID > cur.ID
	case list.SortUpdatedDesc:
		if !r.UpdatedAt.Equal(cur.SortValue) {
			return r.UpdatedAt.Before(cur.SortValue)
		}
		return r.ID > cur.ID
	case list.SortUpdatedAsc:
		if !r.UpdatedAt.Equal(cur.SortValue) {
			return r.UpdatedAt.After(cur.SortValue)
		}
		return r.ID > cur.ID
	case list.SortIDDesc:
		return r.ID < cur.ID
	case list.SortIDAsc:
		return r.ID > cur.ID
	}
	if !r.CreatedAt.Equal(cur.SortValue) {
		return r.CreatedAt.Before(cur.SortValue)
	}
	return r.ID > cur.ID
}

// nextCursor builds the cursor that names `last` under sort s.
// Time-based sorts populate SortValue with the active column; id
// sorts leave SortValue zero (encoder emits an empty string slot).
func nextCursor(last *domain.Document, s list.Sort) *list.Cursor {
	cur := &list.Cursor{Sort: s, ID: last.ID}
	switch s {
	case list.SortCreatedDesc, list.SortCreatedAsc:
		cur.SortValue = last.CreatedAt
	case list.SortUpdatedDesc, list.SortUpdatedAsc:
		cur.SortValue = last.UpdatedAt
	}
	return cur
}

// Links returns both outgoing and incoming links for id. Phase 2 only
// materializes the outgoing set from the document payload itself;
// incoming links are computed by reversing the outgoing map at read
// time (cheap for in-memory, replaced by a proper edge table in the
// Postgres store).
func (s *Store) Links(_ context.Context, id domain.DocumentID) ([]domain.Link, error) {
	if _, ok := s.byID[id]; !ok {
		return nil, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}

	out := make([]domain.Link, 0, len(s.links[id]))
	for _, l := range s.links[id] {
		l.Direction = domain.LinkOutgoing
		out = append(out, l)
	}

	for srcID, srcLinks := range s.links {
		if srcID == id {
			continue
		}
		for _, l := range srcLinks {
			if l.Target != id {
				continue
			}
			out = append(out, domain.Link{
				Direction: domain.LinkIncoming,
				Target:    srcID,
				TargetURL: l.TargetURL,
				Label:     l.Label,
			})
		}
	}
	return out, nil
}

// Discussion returns the discussion block from the document, or zero
// value when none was seeded.
func (s *Store) Discussion(_ context.Context, id domain.DocumentID) (domain.Discussion, error) {
	doc, ok := s.byID[id]
	if !ok {
		return domain.Discussion{}, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}
	if doc.Discussion == nil {
		return domain.Discussion{}, nil
	}
	return *doc.Discussion, nil
}

// Authors returns the authors list from the document.
func (s *Store) Authors(_ context.Context, id domain.DocumentID) ([]domain.Author, error) {
	doc, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}
	return append([]domain.Author(nil), doc.Authors...), nil
}

// Upsert is a Phase-3 stub matching the Postgres implementation — the
// memory store is read-only by construction; IMPL-0003's worker will
// not touch it (the Postgres store replaces memory before the worker
// lands per IMPL-0002 Phase 5).
func (*Store) Upsert(_ context.Context, doc *domain.Document) error {
	return fmt.Errorf(
		"upsert %s: not implemented: memory store is seed-only (IMPL-0003 uses postgres)",
		doc.ID,
	)
}

// Revisions is a stub until the worker lands — every document's
// history has a single synthetic entry pointing at the seed commit
// so frontends can render the endpoint shape end-to-end.
func (s *Store) Revisions(_ context.Context, id domain.DocumentID) ([]store.Revision, error) {
	doc, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}
	var author domain.Author
	if len(doc.Authors) > 0 {
		author = doc.Authors[0]
	}
	return []store.Revision{{
		Commit:    doc.Source.Commit,
		Message:   "initial seed",
		Author:    author,
		CreatedAt: doc.CreatedAt,
		ID:        doc.ID,
	}}, nil
}
