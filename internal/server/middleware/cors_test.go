package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/middleware"
)

func corsCfg() *middleware.CORSConfig {
	cfg := middleware.DefaultCORS([]string{"https://rfc-site.example"})
	return &cfg
}

func TestCORS_NoOrigin_PassThrough(t *testing.T) {
	h := middleware.CORS(corsCfg())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("want no ACAO for missing Origin, got %q", got)
	}
}

func TestCORS_AllowedOrigin_Reflects(t *testing.T) {
	h := middleware.CORS(corsCfg())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody)
	req.Header.Set("Origin", "https://rfc-site.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://rfc-site.example" {
		t.Errorf("ACAO = %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q", got)
	}
}

func TestCORS_DisallowedOrigin_PassThrough(t *testing.T) {
	h := middleware.CORS(corsCfg())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("origin should not be reflected for disallowed origin")
	}
}

func TestCORS_Preflight_204(t *testing.T) {
	h := middleware.CORS(corsCfg())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("inner handler should not be invoked on preflight")
	}))
	req := httptest.NewRequestWithContext(t.Context(), "OPTIONS", "/x", http.NoBody)
	req.Header.Set("Origin", "https://rfc-site.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("missing Allow-Methods")
	}
}
