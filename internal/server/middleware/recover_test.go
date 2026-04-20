package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/middleware"
)

func TestRecover_CatchesPanicAndWrites500(t *testing.T) {
	t.Parallel()

	h := middleware.Recover(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("something went wrong: secret=hunter2")
	}))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/docs", http.NoBody)
	rr := httptest.NewRecorder()

	// Must not re-panic.
	h.ServeHTTP(rr, r)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	// The panic value must NOT surface to the client -- detail safety.
	if strings.Contains(rr.Body.String(), "hunter2") {
		t.Errorf("500 response body leaks panic value: %s", rr.Body.String())
	}
}

func TestRecover_PassesThroughNonPanics(t *testing.T) {
	t.Parallel()

	h := middleware.Recover(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	}))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	if rr.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rr.Code)
	}
	if rr.Body.String() != "ok" {
		t.Errorf("body = %q, want %q", rr.Body.String(), "ok")
	}
}
