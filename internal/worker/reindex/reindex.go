// Package reindex owns the `reindex` and `search_delete` job kinds.
//
// Flow:
//
//	ingest handler --(upsert)--> enqueue reindex  --> this handler --> Meili upsert
//	ingest handler --(delete)--> enqueue search_delete --> this handler --> Meili delete
//
// The handlers re-read the source-of-truth Postgres row (reindex) or
// simply accept an id (search_delete). Keeping the payload to an id
// lets the scanner or reindex command enqueue bulk reindexes without
// baking document bodies into the jobs table.
package reindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/worker/queue"
)

// KindReindex is the job kind for a reindex of one document's content.
const KindReindex = "reindex"

// KindSearchDelete is the job kind for removing a document from the
// search index.
const KindSearchDelete = "search_delete"

// Store is the narrow Postgres read surface the handler uses.
type Store interface {
	Get(ctx context.Context, id domain.DocumentID) (domain.Document, error)
}

// Indexer is the Meilisearch write surface. Implemented by
// internal/search/meilisearch.Indexer; abstracted here so tests can
// plug a fake without a live Meili.
type Indexer interface {
	Upsert(ctx context.Context, doc *domain.Document) error
	Delete(ctx context.Context, id domain.DocumentID) error
}

// Payload is the shared JSON body for both job kinds. Keeping one
// shape across kinds keeps the enqueue path trivial: the scanner /
// ingest handler writes the same `{document_id}` map.
type Payload struct {
	DocumentID string `json:"document_id"`
}

// Handler bundles the store + indexer. One instance serves both
// kinds; the leaser registers its Reindex + Delete methods on
// separate kind entries.
type Handler struct {
	store   Store
	indexer Indexer
	logger  *slog.Logger
}

// Config groups the runtime dependencies.
type Config struct {
	Store   Store
	Indexer Indexer
	Logger  *slog.Logger
}

// New returns a Handler. Nil deps surface as errors at construction.
func New(cfg *Config) (*Handler, error) {
	if cfg == nil {
		return nil, errors.New("reindex: nil config")
	}
	if cfg.Store == nil {
		return nil, errors.New("reindex: nil store")
	}
	if cfg.Indexer == nil {
		return nil, errors.New("reindex: nil indexer")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		store:   cfg.Store,
		indexer: cfg.Indexer,
		logger:  logger.With("component", "reindex"),
	}, nil
}

// Reindex handles one `reindex` job: re-read the document from Postgres
// and hand it to the indexer's Upsert path. Errors wrap through so the
// leaser applies backoff and the queue retries on the next poll.
//
//nolint:gocritic // Reindex matches queue.Handler's value-Job signature; can't pass by pointer.
func (h *Handler) Reindex(ctx context.Context, job queue.Job) error {
	var p Payload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("reindex: unmarshal payload: %w", err)
	}
	if p.DocumentID == "" {
		return fmt.Errorf("%w: reindex: document_id required", domain.ErrInvalidInput)
	}

	doc, err := h.store.Get(ctx, domain.DocumentID(p.DocumentID))
	if err != nil {
		// A deletion race (reindex enqueued just before the tombstone
		// landed) is a non-fatal skip: the search_delete job will
		// drain the sub-docs separately.
		if errors.Is(err, domain.ErrNotFound) {
			h.logger.InfoContext(ctx, "reindex skipped: document vanished",
				"document_id", p.DocumentID)
			return nil
		}
		return fmt.Errorf("reindex: get %s: %w", p.DocumentID, err)
	}

	if err := h.indexer.Upsert(ctx, &doc); err != nil {
		return fmt.Errorf("reindex: upsert %s: %w", p.DocumentID, err)
	}
	h.logger.InfoContext(ctx, "reindexed", "document_id", p.DocumentID)
	return nil
}

// Delete handles one `search_delete` job. No Postgres read — the
// tombstone is authoritative — just clear every sub-doc for the
// parent.
//
//nolint:gocritic // matches queue.Handler value-Job signature
func (h *Handler) Delete(ctx context.Context, job queue.Job) error {
	var p Payload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("search_delete: unmarshal payload: %w", err)
	}
	if p.DocumentID == "" {
		return fmt.Errorf("%w: search_delete: document_id required", domain.ErrInvalidInput)
	}
	if err := h.indexer.Delete(ctx, domain.DocumentID(p.DocumentID)); err != nil {
		return fmt.Errorf("search_delete: delete %s: %w", p.DocumentID, err)
	}
	h.logger.InfoContext(ctx, "deleted from index", "document_id", p.DocumentID)
	return nil
}
