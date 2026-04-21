package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// probeTimeout bounds the per-check deadline on the readiness probe so
// a wedged database can't stall /readyz past kubelet's own timeout.
// Short enough that a failing DB trips readiness quickly; generous
// enough that a healthy DB under load never flaps.
const probeTimeout = 2 * time.Second

// Probe is the readiness probe for the Postgres pool. It satisfies
// server.ReadinessProbe without importing that package — the server
// side depends on the structural interface, not this type.
type Probe struct {
	Pool *pgxpool.Pool
}

// Name returns the probe's stable identifier surfaced in /readyz output.
func (Probe) Name() string { return "postgres" }

// Check runs a bounded Ping against the pool. Returns a non-nil error
// when the database is unreachable or responds past probeTimeout.
func (p Probe) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := p.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	return nil
}
