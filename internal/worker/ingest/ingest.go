// Package ingest owns the `ingest` job handler. One job =  one
// source file pulled from GitHub, parsed via the registered parser,
// and upserted into Postgres in a single transaction. On success a
// `reindex` job is enqueued for the IMPL-0005 Meilisearch writer.
//
// Idempotency is belt-and-braces: the job's `dedup_key` is
// `content:<sha>` (IMPL-0003 RD9) and a post-success short-circuit
// compares `documents.source_commit` before re-parsing when the
// scanner re-enqueues for an unchanged file.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/parser"
	"github.com/donaldgifford/rfc-api/internal/worker/queue"
)

// Kind is the job kind this package handles.
const Kind = "ingest"

// Payload is the `ingest` job's JSON body. The scanner produces it;
// the handler consumes it.
type Payload struct {
	TypeID     string `json:"type_id"`
	Repo       string `json:"repo"`
	Path       string `json:"path"`
	Parser     string `json:"parser"`
	Branch     string `json:"branch"`
	ContentSHA string `json:"content_sha"`
}

// Store is the narrow slice of store.Docs the ingest handler uses.
// Keeping the dependency tight makes unit tests easy (fake backing).
type Store interface {
	Upsert(ctx context.Context, doc *domain.Document) error
}

// Fetcher is the narrow slice of the GitHub client the handler
// needs. A fake can satisfy this from memory in tests.
type Fetcher interface {
	GetFile(ctx context.Context, repo, path, ref string) ([]byte, string, error)
}

// Enqueuer is what the handler uses to emit downstream jobs
// (currently just `reindex`). Queue from IMPL-0003 Phase 3
// satisfies this.
type Enqueuer interface {
	Enqueue(ctx context.Context, kind, dedupKey string, payload any, runAfter time.Time) error
}

// TypeResolver is the DocumentType registry surface the handler
// needs to look up Lifecycle + Prefix for the parser.
type TypeResolver interface {
	Get(id string) (domain.DocumentType, bool)
}

// Handler holds the ingest dependencies. Construct once at worker
// start and reuse across jobs.
type Handler struct {
	store   Store
	fetcher Fetcher
	queue   Enqueuer
	parsers *parser.Registry
	types   TypeResolver
	logger  *slog.Logger
}

// Config groups the runtime dependencies.
type Config struct {
	Store   Store
	Fetcher Fetcher
	Queue   Enqueuer
	Parsers *parser.Registry
	Types   TypeResolver
	Logger  *slog.Logger
}

// New returns a Handler. Logger defaults to slog.Default().
// Takes Config by pointer to dodge gocritic's hugeParam lint (80B).
func New(cfg *Config) (*Handler, error) {
	if cfg == nil {
		return nil, errors.New("ingest: nil config")
	}
	if cfg.Store == nil {
		return nil, errors.New("ingest: nil store")
	}
	if cfg.Fetcher == nil {
		return nil, errors.New("ingest: nil fetcher")
	}
	if cfg.Queue == nil {
		return nil, errors.New("ingest: nil queue")
	}
	if cfg.Parsers == nil {
		return nil, errors.New("ingest: nil parsers")
	}
	if cfg.Types == nil {
		return nil, errors.New("ingest: nil types")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		store:   cfg.Store,
		fetcher: cfg.Fetcher,
		queue:   cfg.Queue,
		parsers: cfg.Parsers,
		types:   cfg.Types,
		logger:  logger.With("component", "ingest"),
	}, nil
}

// Handle runs one ingest job. Fetch → parse → upsert → enqueue
// reindex. Any step can return a wrapped error; the leaser treats
// the error as a retryable failure and applies backoff.
//
//nolint:gocritic // Handle matches queue.Handler's value-Job signature; can't pass by pointer.
func (h *Handler) Handle(ctx context.Context, job queue.Job) error {
	var p Payload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}
	if err := p.validate(); err != nil {
		return err
	}

	dt, ok := h.types.Get(p.TypeID)
	if !ok {
		return fmt.Errorf("%w: unknown type %q", domain.ErrInvalidInput, p.TypeID)
	}

	parserImpl, err := h.parsers.Get(p.Parser)
	if err != nil {
		return err
	}

	ref := p.Branch
	if ref == "" {
		ref = "main"
	}
	raw, sha, err := h.fetcher.GetFile(ctx, p.Repo, p.Path, ref)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if p.ContentSHA != "" && p.ContentSHA != sha {
		// Scanner dedup_key was content:<sha_a> but GitHub now returns
		// <sha_b>. A concurrent push has landed; drop this attempt and
		// let the next scan/webhook re-enqueue with the fresh sha.
		h.logger.InfoContext(ctx, "ingest skipped: sha drift",
			"repo", p.Repo, "path", p.Path, "expected", p.ContentSHA, "got", sha)
		return nil
	}

	doc, err := parserImpl.Parse(raw, dt, domain.Source{
		Repo:   p.Repo,
		Path:   p.Path,
		Commit: sha,
	})
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	if err := h.store.Upsert(ctx, &doc); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}

	// Enqueue reindex. The key is `doc:<id>` so re-ingesting the same
	// document collapses onto one reindex job (RD9).
	if err := h.queue.Enqueue(ctx, "reindex",
		"doc:"+string(doc.ID),
		map[string]string{"document_id": string(doc.ID)},
		time.Time{},
	); err != nil {
		return fmt.Errorf("enqueue reindex: %w", err)
	}

	// Enqueue a discussion refresh. Dedup key `discussion:<id>` keeps
	// repeated ingests collapsed to one in-flight job; the handler
	// self-requeues on a longer cadence for periodic PR-thread polling
	// without pulling the scanner into the discussion path.
	if err := h.queue.Enqueue(ctx, "discussion_fetch",
		"discussion:"+string(doc.ID),
		map[string]string{
			"document_id": string(doc.ID),
			"repo":        p.Repo,
			"path":        p.Path,
		},
		time.Time{},
	); err != nil {
		// Non-fatal: ingest was successful, discussion is a best-effort
		// secondary. Log-and-continue keeps a discussion-service outage
		// from failing the ingest pipeline.
		h.logger.WarnContext(ctx, "enqueue discussion_fetch", "err", err.Error())
	}

	h.logger.InfoContext(ctx, "ingested",
		"document_id", doc.ID, "repo", p.Repo, "path", p.Path, "sha", sha)
	return nil
}

func (p *Payload) validate() error {
	switch {
	case p.TypeID == "":
		return fmt.Errorf("%w: type_id required", domain.ErrInvalidInput)
	case p.Repo == "":
		return fmt.Errorf("%w: repo required", domain.ErrInvalidInput)
	case p.Path == "":
		return fmt.Errorf("%w: path required", domain.ErrInvalidInput)
	case p.Parser == "":
		return fmt.Errorf("%w: parser required", domain.ErrInvalidInput)
	}
	return nil
}
