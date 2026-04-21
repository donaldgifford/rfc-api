package meilisearch

import (
	"context"
	"fmt"
	"time"
)

// probeTimeout bounds the per-check deadline on the readiness probe
// so a wedged Meili can't stall /readyz past kubelet's own timeout.
// Same rationale and budget as postgres.Probe.
const probeTimeout = 2 * time.Second

// Probe is a server.ReadinessProbe for Meilisearch. Structural
// interface in server/readiness.go — nothing here imports the server
// package.
type Probe struct {
	Client *Client
}

// Name is the stable identifier surfaced in /readyz output.
func (Probe) Name() string { return "meilisearch" }

// Check runs a bounded Health against the configured client. A
// failure flips /readyz to 503 but does NOT terminate the server —
// the search endpoint degrades to 503/problem+json on demand while
// the rest of the API keeps serving.
func (p Probe) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := p.Client.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	return nil
}
