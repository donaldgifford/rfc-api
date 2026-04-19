// Package server wires the rfc-api HTTP tier: the main-port user
// traffic server, the admin-port ops + pprof server, route
// registration, handler glue, and shared primitives (readiness
// probes, health handlers) they both depend on.
//
// See DESIGN-0001 §Server construction + §Middleware chain + §API
// surface for the overall shape.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// ReadinessProbe reports whether a single dependency is healthy
// enough for the service to take traffic. Implementations are
// registered on the AdminServer at construction; /readyz iterates
// them and aggregates failures.
//
// Name is surfaced in the failure response body so operators can
// see which dep is unhealthy without tailing logs.
type ReadinessProbe interface {
	Name() string
	Check(ctx context.Context) error
}

// AlwaysReady is a trivial probe that always passes. Used as a
// seed in Phase 1 so /readyz is wired end-to-end before real deps
// land. Exported so tests and future bootstrap code can compose
// with it.
type AlwaysReady struct{}

// Name implements ReadinessProbe.
func (AlwaysReady) Name() string { return "always-ready" }

// Check implements ReadinessProbe.
func (AlwaysReady) Check(_ context.Context) error { return nil }

// -- handlers ---------------------------------------------------------

// healthyBody is the 200 response body for /healthz.
var healthyBody = []byte(`{"status":"ok"}`)

// HealthLive is the /healthz handler. 200 with a minimal JSON body
// regardless of dep state -- kubelet liveness means "is the process
// alive," not "is it useful." No probes, no downstream calls, cheap.
func HealthLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(healthyBody); err != nil {
		slog.ErrorContext(r.Context(), "write /healthz body", "err", err.Error())
	}
}

// readyBody is the success payload for /readyz. "ready" (not "ok")
// so the endpoint's semantic is clear from a curl without the code.
type readyBody struct {
	Status string `json:"status"`
}

// readyFailureBody is the 503 payload listing which probes failed.
type readyFailureBody struct {
	Status   string            `json:"status"` // always "not_ready"
	Failures []readyFailureRow `json:"failures"`
}

type readyFailureRow struct {
	Probe string `json:"probe"`
	Error string `json:"error"`
}

// HealthReady returns the /readyz handler for the given probes.
// Returns 200 when every probe passes; 503 with a failures array
// listing probe names + error strings otherwise.
//
// Probes run sequentially; the endpoint is not rate-limited but is
// cheap enough that parallelism isn't worth the added failure-mode
// surface. A failing probe does NOT short-circuit -- the response
// body lists every failing probe so one kubectl describe tells you
// about multiple unhealthy deps at once.
func HealthReady(probes []ReadinessProbe) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		failures := make([]readyFailureRow, 0)
		for _, p := range probes {
			if err := p.Check(r.Context()); err != nil {
				failures = append(failures, readyFailureRow{
					Probe: p.Name(),
					Error: err.Error(),
				})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)

		if len(failures) == 0 {
			w.WriteHeader(http.StatusOK)
			if err := enc.Encode(readyBody{Status: "ready"}); err != nil {
				slog.ErrorContext(r.Context(), "encode /readyz success", "err", err.Error())
			}
			return
		}

		w.WriteHeader(http.StatusServiceUnavailable)
		if err := enc.Encode(readyFailureBody{
			Status:   "not_ready",
			Failures: failures,
		}); err != nil {
			slog.ErrorContext(r.Context(), "encode /readyz failure", "err", err.Error())
		}
	}
}
