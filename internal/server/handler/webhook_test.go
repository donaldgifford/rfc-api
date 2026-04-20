package handler_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
)

func TestWebhookGitHub_NonPush_AcceptsNoEnqueue(t *testing.T) {
	h := handler.NewWebhook(nil)
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/webhooks/github",
		strings.NewReader(`{"ok":true}`))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", "abc-123")

	rec := httptest.NewRecorder()
	h.GitHub(rec, req)
	if rec.Code != 202 {
		t.Errorf("status = %d, want 202", rec.Code)
	}
}

type recordingQueue struct {
	jobs  atomic.Int32
	last  string
	lastP map[string]string
}

func (q *recordingQueue) Enqueue(_ context.Context, _, dedupKey string, payload any, _ time.Time) error {
	q.jobs.Add(1)
	q.last = dedupKey
	if p, ok := payload.(map[string]string); ok {
		q.lastP = p
	}
	return nil
}

func TestWebhookGitHub_Push_EnqueuesIngest(t *testing.T) {
	q := &recordingQueue{}
	h := handler.NewWebhook(&handler.WebhookConfig{
		Sources: []config.SourceRepo{{
			TypeID: "rfc", Repo: "owner/repo", Path: "docs/rfc/",
			Parser: "docz-markdown", Branch: "main",
		}},
		Queue: q,
	})

	body := `{
		"ref":"refs/heads/main",
		"repository":{"full_name":"owner/repo","default_branch":"main"},
		"head_commit":{"id":"headsha"},
		"commits":[{
			"id":"c1",
			"added":["docs/rfc/0005-new.md"],
			"modified":["docs/rfc/0001-existing.md","docs/unrelated.md"],
			"removed":[]
		}]
	}`
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/webhooks/github",
		strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "del-1")

	rec := httptest.NewRecorder()
	h.GitHub(rec, req)
	if rec.Code != 202 {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	// Two touched paths match the source (the third is outside docs/rfc/).
	if got := q.jobs.Load(); got != 2 {
		t.Errorf("enqueued = %d, want 2", got)
	}
	if q.lastP["type_id"] != "rfc" {
		t.Errorf("payload type_id = %q", q.lastP["type_id"])
	}
	if q.lastP["branch"] != "main" {
		t.Errorf("payload branch = %q", q.lastP["branch"])
	}
}

func TestWebhookGitHub_Push_UnknownRepo_NoOp(t *testing.T) {
	q := &recordingQueue{}
	h := handler.NewWebhook(&handler.WebhookConfig{
		Sources: []config.SourceRepo{{
			TypeID: "rfc", Repo: "owner/repo", Path: "docs/rfc/", Parser: "x",
		}},
		Queue: q,
	})

	body := `{
		"ref":"refs/heads/main",
		"repository":{"full_name":"someone/else"},
		"head_commit":{"id":"a"},
		"commits":[{"added":["docs/rfc/0001.md"]}]
	}`
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/webhooks/github",
		strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")

	rec := httptest.NewRecorder()
	h.GitHub(rec, req)
	if rec.Code != 202 {
		t.Errorf("status = %d", rec.Code)
	}
	if q.jobs.Load() != 0 {
		t.Errorf("unexpected enqueue on unknown repo")
	}
}

func TestWebhookGitHub_MalformedJSON_Still202(t *testing.T) {
	h := handler.NewWebhook(&handler.WebhookConfig{
		Sources: []config.SourceRepo{{TypeID: "rfc", Repo: "o/r", Path: "docs/"}},
		Queue:   &recordingQueue{},
	})
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/webhooks/github",
		strings.NewReader("{not json"))
	req.Header.Set("X-GitHub-Event", "push")
	rec := httptest.NewRecorder()
	h.GitHub(rec, req)
	if rec.Code != 202 {
		t.Errorf("status = %d, want 202 even on malformed JSON", rec.Code)
	}
}

func TestWebhookGitHub_RemovedFile_DoesNotEnqueue(t *testing.T) {
	q := &recordingQueue{}
	h := handler.NewWebhook(&handler.WebhookConfig{
		Sources: []config.SourceRepo{{
			TypeID: "rfc", Repo: "owner/repo", Path: "docs/rfc/", Parser: "x",
		}},
		Queue: q,
	})

	body := `{
		"ref":"refs/heads/main",
		"repository":{"full_name":"owner/repo"},
		"head_commit":{"id":"h"},
		"commits":[{"removed":["docs/rfc/0001.md"]}]
	}`
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/webhooks/github",
		strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	rec := httptest.NewRecorder()
	h.GitHub(rec, req)
	if q.jobs.Load() != 0 {
		t.Errorf("removed-only push should not enqueue; got %d", q.jobs.Load())
	}
}
