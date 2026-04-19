package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/middleware"
	"github.com/donaldgifford/rfc-api/internal/server/reqctx"
)

func TestRequestID_ClientHeaderWins(t *testing.T) {
	t.Parallel()

	const supplied = "client-supplied-01HX"

	var gotCtxID string
	h := middleware.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotCtxID = reqctx.ID(r.Context())
	}))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	r.Header.Set(middleware.HeaderRequestID, supplied)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	if gotCtxID != supplied {
		t.Errorf("ctx id = %q, want %q", gotCtxID, supplied)
	}
	if got := rr.Header().Get(middleware.HeaderRequestID); got != supplied {
		t.Errorf("response header = %q, want %q", got, supplied)
	}
}

func TestRequestID_GeneratesWhenAbsent(t *testing.T) {
	t.Parallel()

	var gotCtxID string
	h := middleware.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotCtxID = reqctx.ID(r.Context())
	}))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	if gotCtxID == "" {
		t.Fatal("ctx id empty, want generated id")
	}
	// 16 bytes hex = 32 chars. The rand-failed fallback is prefixed
	// "rand-failed-" which is obviously longer and distinct, so a
	// strict 32-char check catches both the happy path and regressions.
	if len(gotCtxID) != 32 {
		t.Errorf("generated id len = %d, want 32 (got %q)", len(gotCtxID), gotCtxID)
	}
	if got := rr.Header().Get(middleware.HeaderRequestID); got != gotCtxID {
		t.Errorf("response header = %q, want %q", got, gotCtxID)
	}
}

func TestRequestID_UniqueAcrossRequests(t *testing.T) {
	t.Parallel()

	h := middleware.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))

	seen := make(map[string]struct{}, 5)
	for range 5 {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		id := rr.Header().Get(middleware.HeaderRequestID)
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate request id generated: %q", id)
		}
		seen[id] = struct{}{}
	}
}
