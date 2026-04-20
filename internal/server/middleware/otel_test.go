package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/donaldgifford/rfc-api/internal/server/middleware"
)

// OTel middleware with a no-op provider should be transparent:
// requests flow through, status code preserved, no panics.
func TestOTel_PassthroughWithNoopProvider(t *testing.T) {
	t.Parallel()

	h := middleware.OTel(noop.NewTracerProvider())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("brew"))
		}),
	)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	if rr.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rr.Code)
	}
	if rr.Body.String() != "brew" {
		t.Errorf("body = %q, want %q", rr.Body.String(), "brew")
	}
}
