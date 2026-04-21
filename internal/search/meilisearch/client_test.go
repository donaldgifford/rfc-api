package meilisearch_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/search/meilisearch"
)

// fakeMeili spins an httptest server that captures the Authorization
// header for assertions and serves a /health stub. Each test passes
// a handler to wire additional endpoints as needed.
func fakeMeili(t *testing.T, seenAuth *atomic.Value) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if seenAuth != nil {
			seenAuth.Store(r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "available"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestNewReadClient_RejectsEmptyURL(t *testing.T) {
	_, err := meilisearch.NewReadClient(config.Meili{MasterKey: "k"})
	if err == nil {
		t.Fatal("want error when URL is empty")
	}
	if !strings.Contains(err.Error(), "MEILI_URL") {
		t.Errorf("error = %v, want MEILI_URL in message", err)
	}
}

func TestNewReadClient_RejectsMissingKey(t *testing.T) {
	_, err := meilisearch.NewReadClient(config.Meili{URL: "http://x"})
	if err == nil {
		t.Fatal("want error when no key resolved")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("error = %v", err)
	}
}

func TestNewReadClient_UsesAPIKey(t *testing.T) {
	var seen atomic.Value
	srv := fakeMeili(t, &seen)
	c, err := meilisearch.NewReadClient(config.Meili{
		URL:       srv.URL,
		APIKey:    "read-only",
		MasterKey: "master",
	})
	if err != nil {
		t.Fatalf("NewReadClient: %v", err)
	}
	if err := c.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := seen.Load(); got != "Bearer read-only" {
		t.Errorf("Authorization = %v, want Bearer read-only", got)
	}
}

func TestNewWriteClient_FallsBackToMaster(t *testing.T) {
	var seen atomic.Value
	srv := fakeMeili(t, &seen)
	c, err := meilisearch.NewWriteClient(config.Meili{
		URL:       srv.URL,
		MasterKey: "master-dev-key",
	})
	if err != nil {
		t.Fatalf("NewWriteClient: %v", err)
	}
	if err := c.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := seen.Load(); got != "Bearer master-dev-key" {
		t.Errorf("Authorization = %v, want Bearer master-dev-key (master-key fallback)", got)
	}
}

func TestNewWriteClient_PrefersWriteKey(t *testing.T) {
	var seen atomic.Value
	srv := fakeMeili(t, &seen)
	c, err := meilisearch.NewWriteClient(config.Meili{
		URL:       srv.URL,
		MasterKey: "master",
		WriteKey:  "write-scoped",
	})
	if err != nil {
		t.Fatalf("NewWriteClient: %v", err)
	}
	if err := c.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := seen.Load(); got != "Bearer write-scoped" {
		t.Errorf("Authorization = %v, want Bearer write-scoped", got)
	}
}

func TestClient_Ping_ReturnsErrorOn500(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := meilisearch.NewReadClient(config.Meili{URL: srv.URL, MasterKey: "k"})
	if err != nil {
		t.Fatalf("NewReadClient: %v", err)
	}
	if err := c.Ping(t.Context()); err == nil {
		t.Fatal("Ping: want error on 500")
	}
}
