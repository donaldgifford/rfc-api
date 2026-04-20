package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/server/middleware"
)

func TestTimeout_Expires(t *testing.T) {
	var observed error
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			observed = r.Context().Err()
		case <-time.After(200 * time.Millisecond):
		}
		w.WriteHeader(200)
	})
	h := middleware.Timeout(10 * time.Millisecond)(next)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !errors.Is(observed, context.DeadlineExceeded) {
		t.Errorf("want DeadlineExceeded, got %v", observed)
	}
}

func TestTimeout_Disabled(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		if _, ok := r.Context().Deadline(); ok {
			t.Error("deadline should not be attached when d<=0")
		}
	})
	h := middleware.Timeout(0)(next)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody))
	if !called {
		t.Error("handler not invoked")
	}
}
