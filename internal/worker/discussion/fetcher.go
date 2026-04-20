// Package discussion owns the `discussion_fetch` job handler. Each
// job pulls a document's PR review thread from GitHub and upserts the
// discussions + discussion_participants rows. Per IMPL-0003 RD4 the
// cascade is hard-delete-on-force-push: participants are cleared and
// re-inserted on every fetch so a rewritten PR history cannot leave
// stale authors behind.
//
// Two payload shapes are supported. A direct payload carries the
// document id + source path; the handler fetches the PR and writes
// the discussion. A PR-scope payload carries a PR number; the handler
// resolves the PR's touched files against the configured source set
// and fans out per-document direct jobs. The webhook's PR-event path
// produces PR-scope payloads; the ingest handler produces direct
// payloads.
package discussion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/docid"
	"github.com/donaldgifford/rfc-api/internal/worker/githubsource"
	"github.com/donaldgifford/rfc-api/internal/worker/queue"
)

// Kind is the job kind this package handles.
const Kind = "discussion_fetch"

// Payload is the discussion_fetch job body. One of (DocumentID, Path)
// or PRNumber must be non-zero; the handler branches accordingly.
// Repo is always required.
type Payload struct {
	DocumentID string `json:"document_id,omitempty"`
	Repo       string `json:"repo"`
	Path       string `json:"path,omitempty"`
	PRNumber   int    `json:"pr_number,omitempty"`
}

// Fetcher is the narrow GitHub-access surface the handler needs.
// *githubsource.Client satisfies it; unit tests inject a fake.
type Fetcher interface {
	ListPullRequestsForFile(ctx context.Context, repo, path string) ([]githubsource.PullRequest, error)
	ListPullRequestComments(ctx context.Context, repo string, prNumber int) ([]githubsource.PRComment, error)
	ListPullRequestFiles(ctx context.Context, repo string, prNumber int) ([]string, error)
}

// Store is the persistence surface: write the discussion summary +
// participants for a document. Postgres's *Docs satisfies it.
type Store interface {
	UpsertDiscussion(ctx context.Context, id domain.DocumentID, disc domain.Discussion) error
}

// Enqueuer is used by the handler's PR-scope path to fan out per-
// document direct jobs.
type Enqueuer interface {
	Enqueue(ctx context.Context, kind, dedupKey string, payload any, runAfter time.Time) error
}

// Handler is the dispatch target for discussion_fetch jobs.
type Handler struct {
	store    Store
	fetcher  Fetcher
	queue    Enqueuer
	sources  []config.SourceRepo
	logger   *slog.Logger
	active   time.Duration
	archived time.Duration
}

// Config wires runtime deps. Active / Archived tune self-requeue
// cadence; zero falls back to 1h / 24h.
type Config struct {
	Store    Store
	Fetcher  Fetcher
	Queue    Enqueuer
	Sources  []config.SourceRepo
	Logger   *slog.Logger
	Active   time.Duration
	Archived time.Duration
}

// New returns a Handler. Config by pointer per the hugeParam lint.
func New(cfg *Config) (*Handler, error) {
	if cfg == nil {
		return nil, errors.New("discussion: nil config")
	}
	if cfg.Store == nil {
		return nil, errors.New("discussion: nil store")
	}
	if cfg.Fetcher == nil {
		return nil, errors.New("discussion: nil fetcher")
	}
	if cfg.Queue == nil {
		return nil, errors.New("discussion: nil queue")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	active := cfg.Active
	if active <= 0 {
		active = time.Hour
	}
	archived := cfg.Archived
	if archived <= 0 {
		archived = 24 * time.Hour
	}
	return &Handler{
		store:    cfg.Store,
		fetcher:  cfg.Fetcher,
		queue:    cfg.Queue,
		sources:  cfg.Sources,
		logger:   logger.With("component", "discussion"),
		active:   active,
		archived: archived,
	}, nil
}

// Handle dispatches based on payload shape. A PR-scope payload
// expands into per-document direct jobs; a direct payload runs the
// fetch → upsert flow for a single document.
//
//nolint:gocritic // Handle matches queue.Handler's value-Job signature; can't pass by pointer.
func (h *Handler) Handle(ctx context.Context, job queue.Job) error {
	var p Payload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}
	if p.Repo == "" {
		return fmt.Errorf("%w: repo required", domain.ErrInvalidInput)
	}
	if p.PRNumber > 0 {
		return h.expandPR(ctx, &p)
	}
	if p.DocumentID == "" || p.Path == "" {
		return fmt.Errorf("%w: document_id + path or pr_number required", domain.ErrInvalidInput)
	}
	return h.fetchOne(ctx, &p)
}

// fetchOne runs the direct path: find the PR for a doc, pull the
// comments, upsert. Missing PRs (new doc not yet merged) succeed
// without writing.
func (h *Handler) fetchOne(ctx context.Context, p *Payload) error {
	prs, err := h.fetcher.ListPullRequestsForFile(ctx, p.Repo, p.Path)
	if err != nil {
		return fmt.Errorf("list prs: %w", err)
	}
	if len(prs) == 0 {
		h.logger.InfoContext(ctx, "no prs found for document",
			"document_id", p.DocumentID, "repo", p.Repo, "path", p.Path)
		return nil
	}
	// Use the most-recent PR the commit history yielded; closed/merged
	// PRs still carry the canonical review thread for archived docs.
	pr := prs[0]

	comments, err := h.fetcher.ListPullRequestComments(ctx, p.Repo, pr.Number)
	if err != nil {
		return fmt.Errorf("list comments: %w", err)
	}

	disc := buildDiscussion(pr, comments)
	if err := h.store.UpsertDiscussion(ctx, domain.DocumentID(p.DocumentID), disc); err != nil {
		return fmt.Errorf("upsert discussion: %w", err)
	}

	h.logger.InfoContext(ctx, "discussion synced",
		"document_id", p.DocumentID,
		"pr", pr.Number,
		"comment_count", disc.CommentCount,
		"participants", len(disc.Participants),
	)
	// Self-requeue to keep the discussion fresh. Merged PRs back off
	// to `archived` cadence — no new comments expected, don't burn
	// API quota. Active PRs re-check every `active` interval.
	delay := h.active
	if pr.Merged || strings.EqualFold(pr.State, "closed") {
		delay = h.archived
	}
	if err := h.queue.Enqueue(ctx, Kind,
		"discussion:"+p.DocumentID, p, time.Now().Add(delay),
	); err != nil {
		h.logger.WarnContext(ctx, "requeue discussion", "err", err.Error())
	}
	return nil
}

// expandPR is the PR-scope path. The webhook produces these when a
// PR review event fires; the handler lists the PR's touched files,
// matches them against configured sources, and enqueues one direct
// job per matched document.
func (h *Handler) expandPR(ctx context.Context, p *Payload) error {
	files, err := h.fetcher.ListPullRequestFiles(ctx, p.Repo, p.PRNumber)
	if err != nil {
		return fmt.Errorf("list pr files: %w", err)
	}
	enqueued := 0
	for _, path := range files {
		src, ok := matchSource(h.sources, p.Repo, path)
		if !ok {
			continue
		}
		id := canonicalFromPath(&src, path)
		if id == "" {
			continue
		}
		direct := Payload{
			DocumentID: string(id),
			Repo:       p.Repo,
			Path:       path,
		}
		if err := h.queue.Enqueue(ctx, Kind,
			"discussion:"+string(id), direct, time.Time{},
		); err != nil {
			h.logger.WarnContext(ctx, "enqueue direct",
				"id", id, "err", err.Error())
			continue
		}
		enqueued++
	}
	h.logger.InfoContext(ctx, "pr-scope expansion",
		"repo", p.Repo, "pr", p.PRNumber,
		"files", len(files), "enqueued", enqueued,
	)
	return nil
}

// buildDiscussion aggregates comments into a domain.Discussion.
// Participants are dedup'd by handle in first-seen order.
func buildDiscussion(pr githubsource.PullRequest, comments []githubsource.PRComment) domain.Discussion {
	var latest time.Time
	seen := make(map[string]struct{}, len(comments))
	participants := make([]domain.Author, 0, len(comments))
	for _, c := range comments {
		if c.UpdatedAt.After(latest) {
			latest = c.UpdatedAt
		}
		h := c.Author.Handle
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		participants = append(participants, domain.Author{
			Name:   c.Author.Name,
			Email:  c.Author.Email,
			Handle: h,
		})
	}
	return domain.Discussion{
		URL:          pr.URL,
		CommentCount: len(comments),
		LastActivity: latest,
		Participants: participants,
	}
}

// matchSource mirrors the webhook handler's filter: repo match,
// path-prefix under SourceRepo.Path, and a .md suffix.
func matchSource(sources []config.SourceRepo, repo, path string) (config.SourceRepo, bool) {
	for _, src := range sources {
		if src.Repo != repo {
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

// canonicalFromPath derives the doc id from a source-relative path
// the same way scanner.canonicalFromPath does (see its doc comment
// for why this heuristic lives in the caller rather than the parser).
func canonicalFromPath(src *config.SourceRepo, path string) domain.DocumentID {
	rel := strings.TrimPrefix(strings.TrimPrefix(path, src.Path), "/")
	name := rel
	if slash := strings.IndexByte(rel, '/'); slash >= 0 {
		name = rel[:slash]
	}
	for i := 0; i+4 <= len(name); i++ {
		if isDigits(name[i : i+4]) {
			return docid.Canonical(src.TypeID, name[i:i+4])
		}
	}
	return ""
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
