// Package scanner owns the periodic sweep of each configured
// SourceRepo. On each tick it lists the files in the source path,
// diffs against `documents.source_commit` per document, and enqueues
// an `ingest` job for anything new or changed. Missing files are
// deleted (IMPL-0003 RD4 — hard delete).
//
// The scanner is the "catch-all" path in RFC-0001 Sync: webhooks
// cover the low-latency case, but a scanner pass at cfg.ScannerInterval
// catches webhook misses and rebuilds-from-Git scenarios.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/docid"
	"github.com/donaldgifford/rfc-api/internal/worker/githubsource"
	"github.com/donaldgifford/rfc-api/internal/worker/ingest"
)

// Fetcher enumerates files from a source repo.
type Fetcher interface {
	ListFiles(ctx context.Context, repo, path, ref string) ([]githubsource.File, error)
}

// Store is the read surface the scanner touches to compute the diff
// set. Kept narrow so tests don't need a full store.Docs.
type Store interface {
	ExistingSources(ctx context.Context, repo, basePath string) (map[string]string, error)
	Delete(ctx context.Context, id domain.DocumentID) error
}

// Enqueuer is the queue surface the scanner writes to.
type Enqueuer interface {
	Enqueue(ctx context.Context, kind, dedupKey string, payload any, runAfter time.Time) error
}

// Scanner iterates every configured SourceRepo on a ticker.
type Scanner struct {
	sources  []config.SourceRepo
	fetcher  Fetcher
	store    Store
	queue    Enqueuer
	interval time.Duration
	logger   *slog.Logger
	onScan   func(time.Time)
}

// Config wires the scanner. OnScan, when non-nil, fires after each
// successful pass — the worker's readiness probe reads this
// watermark.
type Config struct {
	Sources  []config.SourceRepo
	Fetcher  Fetcher
	Store    Store
	Queue    Enqueuer
	Interval time.Duration
	Logger   *slog.Logger
	OnScan   func(time.Time)
}

// New returns a Scanner. An empty source list is legal — the
// scanner's Run ticks but no-ops. Config is taken by pointer
// (hugeParam lint, 96B).
func New(cfg *Config) (*Scanner, error) {
	if cfg == nil {
		return nil, errors.New("scanner: nil config")
	}
	if cfg.Fetcher == nil {
		return nil, errors.New("scanner: nil fetcher")
	}
	if cfg.Store == nil {
		return nil, errors.New("scanner: nil store")
	}
	if cfg.Queue == nil {
		return nil, errors.New("scanner: nil queue")
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Scanner{
		sources:  cfg.Sources,
		fetcher:  cfg.Fetcher,
		store:    cfg.Store,
		queue:    cfg.Queue,
		interval: interval,
		logger:   logger.With("component", "scanner"),
		onScan:   cfg.OnScan,
	}, nil
}

// Run ticks until ctx is canceled. Each tick runs one Pass; a
// failing pass is logged at ERROR but doesn't halt the loop —
// transient upstream failures shouldn't turn into worker outages.
func (s *Scanner) Run(ctx context.Context) error {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	// Run once immediately so a fresh worker doesn't wait a full
	// interval before its first scan.
	if err := s.Pass(ctx); err != nil {
		s.logger.ErrorContext(ctx, "initial scan", "err", err.Error())
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := s.Pass(ctx); err != nil {
				s.logger.ErrorContext(ctx, "scan pass", "err", err.Error())
			}
		}
	}
}

// Pass runs one full sweep. Exposed so tests can drive the scanner
// synchronously.
func (s *Scanner) Pass(ctx context.Context) error {
	if len(s.sources) == 0 {
		if s.onScan != nil {
			s.onScan(time.Now())
		}
		return nil
	}
	var firstErr error
	for i := range s.sources {
		if err := s.scanSource(ctx, &s.sources[i]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil && s.onScan != nil {
		s.onScan(time.Now())
	}
	return firstErr
}

func (s *Scanner) scanSource(ctx context.Context, src *config.SourceRepo) error {
	ref := src.Branch
	if ref == "" {
		ref = "main"
	}

	files, err := s.fetcher.ListFiles(ctx, src.Repo, src.Path, ref)
	if err != nil {
		return fmt.Errorf("list %s:%s: %w", src.Repo, src.Path, err)
	}
	// Index remote files by repo-relative path for the diff below.
	remote := make(map[string]githubsource.File, len(files))
	for _, f := range files {
		remote[f.Path] = f
	}

	existing, err := s.store.ExistingSources(ctx, src.Repo, src.Path)
	if err != nil {
		return fmt.Errorf("existing %s:%s: %w", src.Repo, src.Path, err)
	}

	// Enqueue for new/changed files.
	for path, remoteFile := range remote {
		priorSHA, known := existing[path]
		if known && priorSHA == remoteFile.SHA {
			continue
		}
		payload := ingest.Payload{
			TypeID:     src.TypeID,
			Repo:       src.Repo,
			Path:       remoteFile.Path,
			Parser:     src.Parser,
			Branch:     ref,
			ContentSHA: remoteFile.SHA,
		}
		if err := s.queue.Enqueue(ctx, ingest.Kind,
			"content:"+remoteFile.SHA, payload, time.Time{},
		); err != nil {
			return fmt.Errorf("enqueue %s: %w", remoteFile.Path, err)
		}
	}

	// Hard-delete anything in the store but not in the remote listing.
	// docid derivation from path is a concern left to the caller of
	// ExistingSources; the store returns it under the canonical id
	// already so we delete by id.
	for path := range existing {
		if _, stillThere := remote[path]; stillThere {
			continue
		}
		id := canonicalFromPath(src, path)
		if id == "" {
			continue
		}
		if err := s.store.Delete(ctx, id); err != nil {
			s.logger.WarnContext(ctx, "delete", "id", id, "err", err.Error())
			continue
		}
		// Propagate the tombstone to the search index via a
		// search_delete job (IMPL-0005 Phase 3). Dedup key collapses
		// a storm of delete events for the same id onto one job.
		if err := s.queue.Enqueue(ctx, "search_delete",
			"search-delete:"+string(id),
			map[string]string{"document_id": string(id)},
			time.Time{},
		); err != nil {
			s.logger.WarnContext(ctx, "enqueue search_delete",
				"id", id, "err", err.Error())
		}
	}
	return nil
}

// canonicalFromPath derives the document id from a source-relative
// path. Looks for the first "NNNN" segment after the SourceRepo.Path
// root; a miss returns empty so the caller skips the delete. Keeps
// the scanner decoupled from individual parser id schemes while
// still correctly identifying the vast majority of doc paths.
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
