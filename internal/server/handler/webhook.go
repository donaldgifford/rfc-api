package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/server/reqctx"
)

// WebhookEnqueuer is the queue surface the webhook handler writes
// to. Satisfied by *queue.Queue; kept here as an interface so the
// handler package doesn't import internal/worker/queue (the API
// server doesn't own that dependency).
type WebhookEnqueuer interface {
	Enqueue(ctx context.Context, kind, dedupKey string, payload any, runAfter time.Time) error
}

// Webhook serves POST /api/v1/webhooks/github. HMAC verification
// runs as per-route middleware (see internal/server/middleware/
// githubhmac.go); by the time this handler runs the signature has
// been validated.
//
// Phase 5 behavior: parse the push payload, match commit paths
// against configured SourceRepo.Path entries, and enqueue one
// `ingest` job per touched document path. 202 is returned before
// the worker does any work.
type Webhook struct {
	logger  *slog.Logger
	sources []config.SourceRepo
	queue   WebhookEnqueuer
}

// WebhookConfig bundles the webhook handler's dependencies.
type WebhookConfig struct {
	Logger  *slog.Logger
	Sources []config.SourceRepo
	Queue   WebhookEnqueuer
}

// NewWebhook constructs a Webhook handler. When Queue is nil the
// handler logs the event and returns 202 without enqueuing — this
// keeps existing API-only deployments (no worker) viable.
func NewWebhook(cfg *WebhookConfig) *Webhook {
	if cfg == nil {
		return &Webhook{logger: slog.Default()}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Webhook{
		logger:  logger,
		sources: cfg.Sources,
		queue:   cfg.Queue,
	}
}

// pushPayload is the narrow subset of a GitHub push event the
// handler consumes. Only the fields we need are named; unknown
// keys are ignored.
type pushPayload struct {
	Ref        string         `json:"ref"`
	Repository pushRepository `json:"repository"`
	Commits    []pushCommit   `json:"commits"`
	HeadCommit *pushCommit    `json:"head_commit"`
}

type pushRepository struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

type pushCommit struct {
	ID       string   `json:"id"`
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Removed  []string `json:"removed"`
}

// GitHub serves POST /api/v1/webhooks/github. Always returns 202;
// a malformed payload is logged at WARN but not surfaced as 4xx —
// the signature was valid and GitHub's retry semantics are built
// around non-2xx triggering re-delivery, which for a parse failure
// would just retry forever. The handler prioritizes successful
// ACK for every signed delivery.
func (h *Webhook) GitHub(w http.ResponseWriter, r *http.Request) {
	event := r.Header.Get("X-GitHub-Event")
	delivery := r.Header.Get("X-GitHub-Delivery")

	switch event {
	case "push":
		h.handlePush(r)
	default:
		h.logger.InfoContext(r.Context(), "github webhook accepted (noop event)",
			"github.event", event,
			"github.delivery", delivery,
			"request_id", reqctx.ID(r.Context()),
		)
	}
	w.WriteHeader(http.StatusAccepted)
}

// handlePush reads the body, parses the push payload, matches
// commit paths against configured sources, and enqueues an
// ingest job per touched path. Errors are logged, never returned —
// the HTTP response is always 202.
func (h *Webhook) handlePush(r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.WarnContext(ctx, "webhook: read body", "err", err.Error())
		return
	}

	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logger.WarnContext(ctx, "webhook: unmarshal push", "err", err.Error())
		return
	}

	if h.queue == nil || len(h.sources) == 0 {
		h.logger.InfoContext(ctx, "webhook push accepted (no queue / sources)",
			"repo", payload.Repository.FullName,
			"ref", payload.Ref,
			"commits", len(payload.Commits))
		return
	}

	touched := collectTouchedPaths(&payload)
	enqueued := 0
	for path, sha := range touched {
		src, ok := matchSource(h.sources, payload.Repository.FullName, path)
		if !ok {
			continue
		}
		payload := map[string]string{
			"type_id":     src.TypeID,
			"repo":        src.Repo,
			"path":        path,
			"parser":      src.Parser,
			"branch":      branchFromRef(payload.Ref, src.Branch),
			"content_sha": sha,
		}
		if err := h.queue.Enqueue(ctx, "ingest",
			"content:"+sha, payload, time.Time{}); err != nil {
			h.logger.WarnContext(ctx, "webhook: enqueue",
				"path", path, "err", err.Error())
			continue
		}
		enqueued++
	}

	h.logger.InfoContext(ctx, "webhook push processed",
		"repo", payload.Repository.FullName,
		"ref", payload.Ref,
		"enqueued", enqueued,
		"touched_paths", len(touched),
	)
}

// collectTouchedPaths walks the push payload and builds a
// path → latest-sha map. Later commits override earlier ones in
// the same delivery so the most recent sha wins for each file.
// Removed files map to empty sha — callers skip them (the scanner
// handles removal on the next pass rather than trying to derive
// the document id from the webhook alone).
func collectTouchedPaths(p *pushPayload) map[string]string {
	out := make(map[string]string, 16)

	// Use head_commit sha as the canonical "current" sha; per-commit
	// shas in the push payload are commit SHAs, not blob SHAs, so
	// they can't serve as the idempotency key. The real blob sha
	// gets resolved by the ingest handler when it fetches the file.
	headSHA := ""
	if p.HeadCommit != nil {
		headSHA = p.HeadCommit.ID
	}

	for _, c := range p.Commits {
		for _, path := range c.Added {
			out[path] = headSHA
		}
		for _, path := range c.Modified {
			out[path] = headSHA
		}
		for _, path := range c.Removed {
			delete(out, path)
		}
	}
	return out
}

// matchSource returns the SourceRepo whose Repo matches fullName
// and whose Path prefixes the touched file. Non-matches are
// silently skipped so pushes to unrelated paths don't enqueue
// no-op jobs.
func matchSource(sources []config.SourceRepo, fullName, path string) (config.SourceRepo, bool) {
	for _, src := range sources {
		if src.Repo != fullName {
			continue
		}
		if !strings.HasPrefix(path, src.Path) {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(path), ".md") {
			continue
		}
		return src, true
	}
	return config.SourceRepo{}, false
}

// branchFromRef extracts the branch name from a ref like
// `refs/heads/main`. Falls back to SourceRepo.Branch or "main"
// when the ref doesn't match the expected shape.
func branchFromRef(ref, fallback string) string {
	const prefix = "refs/heads/"
	if strings.HasPrefix(ref, prefix) {
		return strings.TrimPrefix(ref, prefix)
	}
	if fallback != "" {
		return fallback
	}
	return "main"
}
