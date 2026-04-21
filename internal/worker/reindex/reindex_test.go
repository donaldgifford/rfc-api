package reindex_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/worker/queue"
	"github.com/donaldgifford/rfc-api/internal/worker/reindex"
)

type fakeStore struct {
	docs map[domain.DocumentID]domain.Document
}

func (f *fakeStore) Get(_ context.Context, id domain.DocumentID) (domain.Document, error) {
	d, ok := f.docs[id]
	if !ok {
		return domain.Document{}, domain.ErrNotFound
	}
	return d, nil
}

type fakeIndexer struct {
	upserted []domain.DocumentID
	deleted  []domain.DocumentID
	err      error
}

func (f *fakeIndexer) Upsert(_ context.Context, doc *domain.Document) error {
	if f.err != nil {
		return f.err
	}
	f.upserted = append(f.upserted, doc.ID)
	return nil
}

func (f *fakeIndexer) Delete(_ context.Context, id domain.DocumentID) error {
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func newHandler(t *testing.T, store reindex.Store, idx reindex.Indexer) *reindex.Handler {
	t.Helper()
	h, err := reindex.New(&reindex.Config{
		Store:   store,
		Indexer: idx,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func mustPayload(t *testing.T, docID string) []byte {
	t.Helper()
	b, err := json.Marshal(reindex.Payload{DocumentID: docID})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func TestReindex_UpsertsFromStore(t *testing.T) {
	store := &fakeStore{docs: map[domain.DocumentID]domain.Document{
		"RFC-0001": {ID: "RFC-0001", Type: "rfc", Title: "One"},
	}}
	idx := &fakeIndexer{}
	h := newHandler(t, store, idx)

	err := h.Reindex(t.Context(), queue.Job{Payload: mustPayload(t, "RFC-0001")})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if len(idx.upserted) != 1 || idx.upserted[0] != "RFC-0001" {
		t.Errorf("upserted = %v", idx.upserted)
	}
}

func TestReindex_VanishedDoc_IsNotAnError(t *testing.T) {
	// reindex job enqueued but the tombstone landed first — not a
	// retryable failure; the search_delete job will drain the sub-docs.
	store := &fakeStore{docs: map[domain.DocumentID]domain.Document{}}
	idx := &fakeIndexer{}
	h := newHandler(t, store, idx)

	err := h.Reindex(t.Context(), queue.Job{Payload: mustPayload(t, "RFC-0404")})
	if err != nil {
		t.Errorf("Reindex on missing doc = %v, want nil (graceful skip)", err)
	}
	if len(idx.upserted) != 0 {
		t.Errorf("upserted unexpectedly = %v", idx.upserted)
	}
}

func TestReindex_IndexerError_Propagates(t *testing.T) {
	store := &fakeStore{docs: map[domain.DocumentID]domain.Document{
		"RFC-0001": {ID: "RFC-0001", Type: "rfc"},
	}}
	sentinel := errors.New("meili down")
	idx := &fakeIndexer{err: sentinel}
	h := newHandler(t, store, idx)

	err := h.Reindex(t.Context(), queue.Job{Payload: mustPayload(t, "RFC-0001")})
	if err == nil || !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want wrap of sentinel", err)
	}
}

func TestSearchDelete_CallsIndexerDelete(t *testing.T) {
	idx := &fakeIndexer{}
	h := newHandler(t, &fakeStore{}, idx)

	err := h.Delete(t.Context(), queue.Job{Payload: mustPayload(t, "RFC-0007")})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(idx.deleted) != 1 || idx.deleted[0] != "RFC-0007" {
		t.Errorf("deleted = %v", idx.deleted)
	}
}

func TestSearchDelete_RejectsMissingID(t *testing.T) {
	h := newHandler(t, &fakeStore{}, &fakeIndexer{})
	err := h.Delete(t.Context(), queue.Job{Payload: mustPayload(t, "")})
	if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}
