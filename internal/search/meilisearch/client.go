// Package meilisearch wraps the official Meilisearch SDK and exposes
// the Client type used by both processes. The API holds a read-scoped
// client (search only); the worker holds a write-scoped client
// (documents.*, indexes.*, settings.*). Constructors pick the right
// key from config.Meili — see IMPL-0005 Phase 1 + ADR-0003.
package meilisearch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	meili "github.com/meilisearch/meilisearch-go"

	"github.com/donaldgifford/rfc-api/internal/config"
)

// IndexName is the canonical single-index UID per OQ1. Every doc
// carries a `type` filterable attribute; per-type filtering is a
// filter clause, not a separate index.
const IndexName = "documents"

// defaultHTTPTimeout bounds every SDK call. Meili is expected to be
// an in-cluster hop of a few milliseconds; anything over a couple
// seconds is a latent failure we'd rather surface than compound.
const defaultHTTPTimeout = 5 * time.Second

// Client wraps the SDK's ServiceManager so callers never import the
// SDK directly. The wrapper is intentionally thin — every routine in
// this package takes *Client so it can compose deeper flows (indexer,
// settings) without growing the surface area here.
type Client struct {
	svc meili.ServiceManager
	url string
}

// Manager returns the underlying SDK handle. Exposed only so the
// indexer + settings packages can reach the index API without a
// forest of wrapper methods; external callers should not use this.
func (c *Client) Manager() meili.ServiceManager { return c.svc }

// URL returns the configured base URL. Logged once at startup; the
// key is never exposed.
func (c *Client) URL() string { return c.url }

// NewReadClient builds a Meili client scoped to search-only
// operations. APIKey wins; MasterKey is the dev fallback. Empty URL
// is a config error — operators should set MEILI_URL or rely on the
// default.
func NewReadClient(cfg config.Meili) (*Client, error) {
	return newClient(cfg.URL, cfg.ReadKey(), "read")
}

// NewWriteClient builds a Meili client scoped to write operations.
// WriteKey wins; MasterKey is the dev fallback.
func NewWriteClient(cfg config.Meili) (*Client, error) {
	return newClient(cfg.URL, cfg.WriteSecret(), "write")
}

func newClient(url, key, scope string) (*Client, error) {
	if url == "" {
		return nil, fmt.Errorf("meilisearch: %s client: MEILI_URL is empty", scope)
	}
	if key == "" {
		return nil, fmt.Errorf("meilisearch: %s client: no API key configured", scope)
	}
	svc := meili.New(
		url,
		meili.WithAPIKey(key),
		meili.WithCustomClient(&http.Client{Timeout: defaultHTTPTimeout}),
	)
	return &Client{svc: svc, url: url}, nil
}

// Ping verifies the server is reachable. Wraps SDK errors so callers
// can errors.Is against a sentinel.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.svc.HealthWithContext(ctx); err != nil {
		return fmt.Errorf("meilisearch health: %w", err)
	}
	return nil
}

// awaitTask blocks until taskUID terminates, surfacing a failure
// status as an error. The SDK's WaitForTask returns the Task without
// inspecting status — a failed task is still "done" from the poll
// loop's perspective — so callers that care about the write
// succeeding (indexer, settings bootstrap) go through this helper.
func (c *Client) awaitTask(ctx context.Context, taskUID int64, label string) error {
	task, err := c.svc.WaitForTaskWithContext(ctx, taskUID, settingsTaskPoll)
	if err != nil {
		return fmt.Errorf("meilisearch: wait for %s task %d: %w", label, taskUID, err)
	}
	if task == nil {
		return nil
	}
	if task.Status == meili.TaskStatusFailed || task.Status == meili.TaskStatusCanceled {
		msg := ""
		if task.Error.Message != "" {
			msg = task.Error.Message
		}
		return fmt.Errorf("meilisearch: %s task %d %s: %s", label, taskUID, task.Status, msg)
	}
	return nil
}

// ErrUnavailable signals Meilisearch was not reachable or reported
// unhealthy. Classifier-friendly: handlers wrap this into a 503 with
// problem+json body.
var ErrUnavailable = errors.New("meilisearch: unavailable")
