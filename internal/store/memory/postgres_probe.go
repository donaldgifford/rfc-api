package memory

import (
	"context"

	"github.com/donaldgifford/rfc-api/internal/server"
)

// PostgresProbe is the Phase 2 placeholder readiness probe. It always
// reports ready; the real Postgres probe lands with the real store.
// Registered in cmd/rfc-api/serve.go so the readyz plumbing is
// exercised end-to-end even without a database.
type PostgresProbe struct{}

// Name returns the probe's stable identifier shown in /readyz output.
func (PostgresProbe) Name() string { return "postgres" }

// Check always returns nil in Phase 2.
func (PostgresProbe) Check(context.Context) error { return nil }

// Compile-time assertion: PostgresProbe satisfies the probe contract.
var _ server.ReadinessProbe = PostgresProbe{}
