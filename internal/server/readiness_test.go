package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server"
)

// failingProbe satisfies ReadinessProbe, always reports err with the
// given name.
type failingProbe struct {
	name string
	err  error
}

func (p failingProbe) Name() string                  { return p.name }
func (p failingProbe) Check(_ context.Context) error { return p.err }

func TestHealthLive_200(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody)
	rr := httptest.NewRecorder()
	server.HealthLive(rr, r)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if body := strings.TrimSpace(rr.Body.String()); body != `{"status":"ok"}` {
		t.Errorf("body = %q, want %q", body, `{"status":"ok"}`)
	}
}

func TestHealthReady_AllPass(t *testing.T) {
	t.Parallel()

	h := server.HealthReady([]server.ReadinessProbe{server.AlwaysReady{}})

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody)
	rr := httptest.NewRecorder()
	h(rr, r)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%q)", err, rr.Body.String())
	}
	if body.Status != "ready" {
		t.Errorf("status = %q, want %q", body.Status, "ready")
	}
}

func TestHealthReady_OneFails(t *testing.T) {
	t.Parallel()

	probes := []server.ReadinessProbe{
		server.AlwaysReady{},
		failingProbe{name: "postgres", err: errors.New("connection refused")},
	}
	h := server.HealthReady(probes)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody)
	rr := httptest.NewRecorder()
	h(rr, r)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}

	var body struct {
		Status   string `json:"status"`
		Failures []struct {
			Probe string `json:"probe"`
			Error string `json:"error"`
		} `json:"failures"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%q)", err, rr.Body.String())
	}

	if body.Status != "not_ready" {
		t.Errorf("status = %q, want %q", body.Status, "not_ready")
	}
	if len(body.Failures) != 1 {
		t.Fatalf("failures len = %d, want 1 (body=%q)", len(body.Failures), rr.Body.String())
	}
	if body.Failures[0].Probe != "postgres" {
		t.Errorf("failures[0].Probe = %q, want %q", body.Failures[0].Probe, "postgres")
	}
	if !strings.Contains(body.Failures[0].Error, "connection refused") {
		t.Errorf("failures[0].Error = %q, want to contain %q",
			body.Failures[0].Error, "connection refused")
	}
}

func TestHealthReady_MultipleFailures_AllListed(t *testing.T) {
	t.Parallel()

	probes := []server.ReadinessProbe{
		failingProbe{name: "postgres", err: errors.New("a")},
		failingProbe{name: "meilisearch", err: errors.New("b")},
		server.AlwaysReady{},
	}
	h := server.HealthReady(probes)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody)
	rr := httptest.NewRecorder()
	h(rr, r)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}

	var body struct {
		Failures []struct {
			Probe string `json:"probe"`
		} `json:"failures"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Failures) != 2 {
		t.Fatalf("failures len = %d, want 2", len(body.Failures))
	}
	probesFailed := []string{body.Failures[0].Probe, body.Failures[1].Probe}
	wantSet := map[string]bool{"postgres": true, "meilisearch": true}
	for _, p := range probesFailed {
		if !wantSet[p] {
			t.Errorf("unexpected failing probe %q", p)
		}
	}
}

func TestAlwaysReady_Name(t *testing.T) {
	t.Parallel()

	if got := (server.AlwaysReady{}).Name(); got != "always-ready" {
		t.Errorf("Name() = %q, want always-ready", got)
	}
}
