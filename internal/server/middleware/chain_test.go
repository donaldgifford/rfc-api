package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/middleware"
)

// tag returns a Middleware that appends label to an order slice each
// time a request passes through it (on the way in).
func tag(order *[]string, label string) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*order = append(*order, label)
			next.ServeHTTP(w, r)
		})
	}
}

func TestChain_Order(t *testing.T) {
	t.Parallel()

	var got []string

	handler := middleware.Chain(
		tag(&got, "outer"),
		tag(&got, "middle"),
		tag(&got, "inner"),
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got = append(got, "handler")
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	want := []string{"outer", "middle", "inner", "handler"}
	if len(got) != len(want) {
		t.Fatalf("order len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestChain_Empty(t *testing.T) {
	t.Parallel()

	called := false
	h := middleware.Chain()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	if !called {
		t.Fatal("empty chain did not dispatch to handler")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}
