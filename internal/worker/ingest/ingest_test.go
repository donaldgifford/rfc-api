package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/parser"
	"github.com/donaldgifford/rfc-api/internal/worker/ingest"
	"github.com/donaldgifford/rfc-api/internal/worker/queue"
)

// fakeStore captures the last Upsert for assertions.
type fakeStore struct {
	upserted *domain.Document
	err      error
}

func (f *fakeStore) Upsert(_ context.Context, doc *domain.Document) error {
	if f.err != nil {
		return f.err
	}
	f.upserted = doc
	return nil
}

// fakeFetcher returns a canned response.
type fakeFetcher struct {
	content    []byte
	sha        string
	commitTime time.Time
	err        error
}

func (f *fakeFetcher) GetFile(_ context.Context, _, _, _ string) ([]byte, string, error) {
	return f.content, f.sha, f.err
}

func (f *fakeFetcher) CommitTimeForFile(_ context.Context, _, _, _ string) (time.Time, error) {
	return f.commitTime, nil
}

// fakeQueue records enqueued jobs.
type fakeQueue struct {
	enqueued []fakeEnqueue
	err      error
}

type fakeEnqueue struct {
	kind    string
	dedup   string
	payload any
}

func (f *fakeQueue) Enqueue(_ context.Context, kind, dedup string, payload any, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.enqueued = append(f.enqueued, fakeEnqueue{kind, dedup, payload})
	return nil
}

type fakeTypes struct{}

func (fakeTypes) Get(id string) (domain.DocumentType, bool) {
	if id != "rfc" {
		return domain.DocumentType{}, false
	}
	return domain.DocumentType{
		ID:        "rfc",
		Name:      "RFCs",
		Prefix:    "RFC",
		Lifecycle: []string{"Draft", "Proposed", "Accepted"},
	}, true
}

type stubParser struct{}

func (stubParser) Parse(_ []byte, t domain.DocumentType, src domain.Source) (domain.Document, error) {
	return domain.Document{
		ID:     "RFC-0001",
		Type:   t.ID,
		Title:  "Test",
		Status: "Draft",
		Source: src,
	}, nil
}

func newHandler(t *testing.T, store *fakeStore, fetcher *fakeFetcher, q *fakeQueue) *ingest.Handler {
	t.Helper()
	reg := parser.NewRegistry()
	if err := reg.Register("stub", stubParser{}); err != nil {
		t.Fatal(err)
	}
	h, err := ingest.New(&ingest.Config{
		Store:   store,
		Fetcher: fetcher,
		Queue:   q,
		Parsers: reg,
		Types:   fakeTypes{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHandle_HappyPath(t *testing.T) {
	store := &fakeStore{}
	fetcher := &fakeFetcher{content: []byte(""), sha: "abc"}
	q := &fakeQueue{}
	h := newHandler(t, store, fetcher, q)

	payload := ingest.Payload{
		TypeID:     "rfc",
		Repo:       "o/r",
		Path:       "docs/rfc/0001.md",
		Parser:     "stub",
		ContentSHA: "abc",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.Handle(t.Context(), queue.Job{
		ID:      uuid.New(),
		Kind:    "ingest",
		Payload: body,
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if store.upserted == nil || store.upserted.ID != "RFC-0001" {
		t.Errorf("upserted = %+v", store.upserted)
	}
	// Successful ingest fans out two downstream jobs: reindex for the
	// search path (IMPL-0005) and discussion_fetch for the PR thread
	// (IMPL-0003 Phase 6). Order matches the enqueue call order.
	if len(q.enqueued) != 2 {
		t.Fatalf("enqueued = %+v", q.enqueued)
	}
	if q.enqueued[0].kind != "reindex" || q.enqueued[0].dedup != "doc:RFC-0001" {
		t.Errorf("reindex = %+v", q.enqueued[0])
	}
	if q.enqueued[1].kind != "discussion_fetch" || q.enqueued[1].dedup != "discussion:RFC-0001" {
		t.Errorf("discussion_fetch = %+v", q.enqueued[1])
	}
}

func TestHandle_SHADrift_Skips(t *testing.T) {
	store := &fakeStore{}
	fetcher := &fakeFetcher{content: []byte(""), sha: "new-sha"}
	q := &fakeQueue{}
	h := newHandler(t, store, fetcher, q)

	payload := ingest.Payload{
		TypeID:     "rfc",
		Repo:       "o/r",
		Path:       "docs/rfc/0001.md",
		Parser:     "stub",
		ContentSHA: "old-sha", // mismatch -> skip
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.Handle(t.Context(), queue.Job{Payload: body}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if store.upserted != nil {
		t.Errorf("want no upsert on sha drift, got %+v", store.upserted)
	}
	if len(q.enqueued) != 0 {
		t.Errorf("want no reindex on sha drift, got %+v", q.enqueued)
	}
}

func TestHandle_UnknownType_ReturnsInvalidInput(t *testing.T) {
	h := newHandler(t, &fakeStore{}, &fakeFetcher{sha: "x"}, &fakeQueue{})

	payload := ingest.Payload{TypeID: "zzz", Repo: "o/r", Path: "p", Parser: "stub"}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(t.Context(), queue.Job{Payload: body}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestHandle_MalformedPayload_Errors(t *testing.T) {
	h := newHandler(t, &fakeStore{}, &fakeFetcher{}, &fakeQueue{})
	if err := h.Handle(t.Context(), queue.Job{Payload: []byte("{not json")}); err == nil {
		t.Fatal("want error on malformed payload")
	}
}
