package discussion_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/worker/discussion"
	"github.com/donaldgifford/rfc-api/internal/worker/githubsource"
	"github.com/donaldgifford/rfc-api/internal/worker/queue"
)

type fakeFetcher struct {
	prs      []githubsource.PullRequest
	comments []githubsource.PRComment
	files    []string
	err      error
}

func (f *fakeFetcher) ListPullRequestsForFile(_ context.Context, _, _ string) ([]githubsource.PullRequest, error) {
	return f.prs, f.err
}

func (f *fakeFetcher) ListPullRequestComments(_ context.Context, _ string, _ int) ([]githubsource.PRComment, error) {
	return f.comments, f.err
}

func (f *fakeFetcher) ListPullRequestFiles(_ context.Context, _ string, _ int) ([]string, error) {
	return f.files, f.err
}

type fakeStore struct {
	calls []fakeCall
	err   error
}

type fakeCall struct {
	id   domain.DocumentID
	disc domain.Discussion
}

func (s *fakeStore) UpsertDiscussion(_ context.Context, id domain.DocumentID, disc domain.Discussion) error {
	s.calls = append(s.calls, fakeCall{id: id, disc: disc})
	return s.err
}

type fakeQueue struct {
	enqueued []fakeEnqueue
	err      error
}

type fakeEnqueue struct {
	kind     string
	dedup    string
	payload  any
	runAfter time.Time
}

func (q *fakeQueue) Enqueue(_ context.Context, kind, dedup string, payload any, runAfter time.Time) error {
	q.enqueued = append(q.enqueued, fakeEnqueue{kind: kind, dedup: dedup, payload: payload, runAfter: runAfter})
	return q.err
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func baseHandler(t *testing.T, fetcher *fakeFetcher, store *fakeStore, q *fakeQueue) *discussion.Handler {
	t.Helper()
	h, err := discussion.New(&discussion.Config{
		Store:   store,
		Fetcher: fetcher,
		Queue:   q,
		Sources: []config.SourceRepo{{
			TypeID: "rfc", Repo: "owner/repo", Path: "docs/rfc/",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHandle_Direct_WritesDiscussion(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{
		prs: []githubsource.PullRequest{{Number: 42, URL: "https://example.test/pr/42", State: "open"}},
		comments: []githubsource.PRComment{
			{Author: githubsource.Participant{Handle: "alice", Name: "Alice"}, UpdatedAt: now.Add(-time.Hour)},
			{Author: githubsource.Participant{Handle: "bob"}, UpdatedAt: now},
			{Author: githubsource.Participant{Handle: "alice"}, UpdatedAt: now.Add(-2 * time.Hour)},
		},
	}
	store := &fakeStore{}
	q := &fakeQueue{}
	h := baseHandler(t, fetcher, store, q)

	err := h.Handle(t.Context(), queue.Job{
		Kind:    discussion.Kind,
		Payload: mustMarshal(t, discussion.Payload{DocumentID: "RFC-0001", Repo: "owner/repo", Path: "docs/rfc/0001.md"}),
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want 1", len(store.calls))
	}
	got := store.calls[0]
	if got.id != "RFC-0001" {
		t.Errorf("id = %q", got.id)
	}
	if got.disc.URL != "https://example.test/pr/42" {
		t.Errorf("url = %q", got.disc.URL)
	}
	if got.disc.CommentCount != 3 {
		t.Errorf("count = %d", got.disc.CommentCount)
	}
	if len(got.disc.Participants) != 2 {
		t.Errorf("participants dedup failed: %d", len(got.disc.Participants))
	}
	if !got.disc.LastActivity.Equal(now) {
		t.Errorf("last_activity = %v, want %v", got.disc.LastActivity, now)
	}
	// Active PR → self-requeue at ~1h.
	if len(q.enqueued) != 1 {
		t.Fatalf("enqueued = %d, want 1", len(q.enqueued))
	}
	if q.enqueued[0].kind != discussion.Kind {
		t.Errorf("kind = %q", q.enqueued[0].kind)
	}
	if q.enqueued[0].dedup != "discussion:RFC-0001" {
		t.Errorf("dedup = %q", q.enqueued[0].dedup)
	}
}

func TestHandle_Direct_NoPR_NoWrite(t *testing.T) {
	fetcher := &fakeFetcher{}
	store := &fakeStore{}
	q := &fakeQueue{}
	h := baseHandler(t, fetcher, store, q)

	err := h.Handle(t.Context(), queue.Job{
		Payload: mustMarshal(t, discussion.Payload{DocumentID: "RFC-0001", Repo: "owner/repo", Path: "docs/rfc/0001.md"}),
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(store.calls) != 0 {
		t.Errorf("no-PR path should not write, got %d calls", len(store.calls))
	}
	if len(q.enqueued) != 0 {
		t.Errorf("no-PR path should not self-requeue, got %d", len(q.enqueued))
	}
}

func TestHandle_Direct_MergedPR_BacksOff(t *testing.T) {
	fetcher := &fakeFetcher{
		prs: []githubsource.PullRequest{{Number: 1, Merged: true, State: "closed"}},
	}
	store := &fakeStore{}
	q := &fakeQueue{}
	h, err := discussion.New(&discussion.Config{
		Store: store, Fetcher: fetcher, Queue: q,
		Active:   time.Minute,
		Archived: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.Handle(t.Context(), queue.Job{
		Payload: mustMarshal(t, discussion.Payload{DocumentID: "X-1", Repo: "o/r", Path: "p.md"}),
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("enqueued = %d", len(q.enqueued))
	}
	// The run_after should sit roughly at now+24h, a lot longer than
	// the 1-minute active cadence.
	delta := time.Until(q.enqueued[0].runAfter)
	if delta < 23*time.Hour {
		t.Errorf("merged PR should back off to ~24h; run_after delta = %s", delta)
	}
}

func TestHandle_PRExpansion_FansOutPerDoc(t *testing.T) {
	fetcher := &fakeFetcher{
		files: []string{
			"docs/rfc/0001-alpha.md",
			"docs/rfc/0002-beta.md",
			"docs/unrelated.md",
			"README.md",
		},
	}
	store := &fakeStore{}
	q := &fakeQueue{}
	h := baseHandler(t, fetcher, store, q)

	err := h.Handle(t.Context(), queue.Job{
		Payload: mustMarshal(t, discussion.Payload{Repo: "owner/repo", PRNumber: 7}),
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(q.enqueued) != 2 {
		t.Fatalf("enqueued = %d, want 2 (matched files only)", len(q.enqueued))
	}
	got := map[string]bool{}
	for _, e := range q.enqueued {
		got[e.dedup] = true
	}
	if !got["discussion:RFC-0001"] || !got["discussion:RFC-0002"] {
		t.Errorf("unexpected dedup keys: %v", got)
	}
	if len(store.calls) != 0 {
		t.Errorf("PR-expansion path must not write to store directly")
	}
}

func TestHandle_FetcherError_Propagates(t *testing.T) {
	fetcher := &fakeFetcher{err: errors.New("boom")}
	h := baseHandler(t, fetcher, &fakeStore{}, &fakeQueue{})
	err := h.Handle(t.Context(), queue.Job{
		Payload: mustMarshal(t, discussion.Payload{DocumentID: "RFC-1", Repo: "o/r", Path: "p.md"}),
	})
	if err == nil {
		t.Error("want error propagation from fetcher")
	}
}

func TestHandle_MalformedPayload(t *testing.T) {
	h := baseHandler(t, &fakeFetcher{}, &fakeStore{}, &fakeQueue{})
	err := h.Handle(t.Context(), queue.Job{Payload: json.RawMessage("{not-json")})
	if err == nil {
		t.Error("want error on malformed payload")
	}
}

func TestHandle_MissingRequired(t *testing.T) {
	h := baseHandler(t, &fakeFetcher{}, &fakeStore{}, &fakeQueue{})
	err := h.Handle(t.Context(), queue.Job{Payload: mustMarshal(t, discussion.Payload{Repo: "o/r"})})
	if err == nil {
		t.Error("want error when neither doc_id+path nor pr_number supplied")
	}
}

func TestNew_ValidatesDeps(t *testing.T) {
	if _, err := discussion.New(nil); err == nil {
		t.Error("want error on nil config")
	}
	if _, err := discussion.New(&discussion.Config{}); err == nil {
		t.Error("want error on missing store")
	}
}
