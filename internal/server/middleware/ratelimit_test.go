package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/middleware"
)

func TestRateLimit_AllowsBurst(t *testing.T) {
	h := middleware.RateLimit(middleware.RateLimitConfig{
		RPS: 1, Burst: 3, Key: func(*http.Request) string { return "one" },
	})(okHandler())
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody))
		if rec.Code != 200 {
			t.Errorf("request %d: status=%d, want 200", i, rec.Code)
		}
	}
}

func TestRateLimit_RejectsBeyondBurst(t *testing.T) {
	h := middleware.RateLimit(middleware.RateLimitConfig{
		RPS: 1, Burst: 2, Key: func(*http.Request) string { return "one" },
	})(okHandler())
	// Exhaust burst
	for i := 0; i < 2; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Errorf("Retry-After missing")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
}

func TestRateLimit_Disabled(t *testing.T) {
	h := middleware.RateLimit(middleware.RateLimitConfig{})(okHandler())
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody))
		if rec.Code != 200 {
			t.Fatalf("disabled limiter rejected request %d", i)
		}
	}
}

func TestIPKey_XForwardedFor(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody)
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	if got := middleware.IPKey(req); got != "203.0.113.5" {
		t.Errorf("got %q", got)
	}
}

func TestIPKey_RemoteAddrFallback(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody)
	req.RemoteAddr = "10.0.0.1:54321"
	if got := middleware.IPKey(req); got != "10.0.0.1" {
		t.Errorf("got %q", got)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
}
