package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/middleware"
	"github.com/donaldgifford/rfc-api/internal/server/reqctx"
	"github.com/donaldgifford/rfc-api/internal/server/routectx"
)

// withCapturedLogger swaps slog.Default for a JSON handler backed by a
// buffer for the duration of the test. Returns the buffer and a
// cleanup func.
func withCapturedLogger(t *testing.T) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return buf
}

// lastLogRecord returns the last JSON-encoded log record in buf as a
// decoded map. Useful when multiple log lines are emitted but only
// the tested one matters.
func lastLogRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	if len(lines) == 0 {
		t.Fatal("no log records")
	}
	var rec map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &rec); err != nil {
		t.Fatalf("unmarshal log record: %v (line=%q)", err, lines[len(lines)-1])
	}
	return rec
}

func TestLogger_EmitsOTelSemconvFields(t *testing.T) {
	buf := withCapturedLogger(t)

	const reqID = "req-01HX"
	const pattern = "/api/v1/rfc/{id}"

	h := middleware.Logger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("brewed"))
	}))

	ctx := reqctx.WithID(t.Context(), reqID)
	ctx = routectx.With(ctx, "rfc", pattern)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/rfc/0001", http.NoBody)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	rec := lastLogRecord(t, buf)

	checks := []struct {
		key  string
		want any
	}{
		{"http.request.method", "GET"},
		{"url.path", "/api/v1/rfc/0001"},
		{"http.route", pattern},
		{"http.response.status_code", float64(http.StatusTeapot)},
		{"http.response.body.size", float64(6)},
		{"request_id", reqID},
		{"rfc_api.document_type", "rfc"},
	}
	for _, c := range checks {
		got, ok := rec[c.key]
		if !ok {
			t.Errorf("missing key %q; record = %v", c.key, rec)
			continue
		}
		if got != c.want {
			t.Errorf("key %q = %v (%T), want %v (%T)", c.key, got, got, c.want, c.want)
		}
	}
	if dur, ok := rec["http.server.duration"].(float64); !ok || dur < 0 {
		t.Errorf("http.server.duration = %v, want a non-negative float", rec["http.server.duration"])
	}
}

func TestLogger_DefaultsStatusTo200WhenHandlerDoesNotWriteHeader(t *testing.T) {
	buf := withCapturedLogger(t)

	h := middleware.Logger(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	rec := lastLogRecord(t, buf)
	if got, _ := rec["http.response.status_code"].(float64); int(got) != http.StatusOK {
		t.Errorf("status code = %v, want 200 when handler never writes", rec["http.response.status_code"])
	}
}

func TestLogger_OmitsRouteWhenNotSet(t *testing.T) {
	buf := withCapturedLogger(t)

	h := middleware.Logger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	rec := lastLogRecord(t, buf)
	if _, ok := rec["http.route"]; ok {
		t.Errorf("http.route present in record; should be absent when routectx is unset: %v", rec)
	}
}
