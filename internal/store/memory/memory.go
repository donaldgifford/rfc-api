// Package memory is an in-memory implementation of store.Docs,
// seeded from JSON files matching the API wire format. Per IMPL-0001
// the JSON files double as expected-response fixtures in integration
// tests so one corpus backs both the seed and the contract checks.
//
// The store is read-only after construction and safe for concurrent
// reads. Phase 3 replaces it with a real Postgres store; swapping
// should not require any change under internal/server/.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store"
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

// List applies the query against the in-memory index.
func (s *Store) List(_ context.Context, q store.ListQuery) (store.Page, error) {
	if q.Limit <= 0 {
		return store.Page{}, fmt.Errorf("%w: limit must be positive", domain.ErrInvalidInput)
	}

	filtered := make([]domain.Document, 0, len(s.ordered))
	for i := range s.ordered {
		if q.TypeID != "" && s.ordered[i].Type != q.TypeID {
			continue
		}
		filtered = append(filtered, s.ordered[i])
	}
	total := len(filtered)

	// Seek past the cursor: the cursor carries the last row on the
	// previous page; skip everything up to and including that row.
	if q.Cursor != nil {
		idx := sort.Search(len(filtered), func(i int) bool {
			d := filtered[i]
			if !d.CreatedAt.Equal(q.Cursor.CreatedAt) {
				return d.CreatedAt.Before(q.Cursor.CreatedAt)
			}
			return d.ID > q.Cursor.ID
		})
		filtered = filtered[idx:]
	}

	limit := q.Limit
	page := store.Page{Total: total}
	if len(filtered) > limit {
		last := filtered[limit-1]
		page.NextCursor = &store.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
		page.Items = append(page.Items, filtered[:limit]...)
	} else {
		page.Items = append(page.Items, filtered...)
	}
	return page, nil
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
