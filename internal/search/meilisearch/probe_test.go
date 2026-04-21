package meilisearch_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/search/meilisearch"
)

func TestProbe_Name(t *testing.T) {
	if got := (meilisearch.Probe{}).Name(); got != "meilisearch" {
		t.Errorf("Name = %q, want meilisearch", got)
	}
}

func TestProbe_Check_OK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "available"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := meilisearch.NewReadClient(config.Meili{URL: srv.URL, MasterKey: "k"})
	if err != nil {
		t.Fatalf("NewReadClient: %v", err)
	}
	if err := (meilisearch.Probe{Client: c}).Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestProbe_Check_ErrorWrapped(t *testing.T) {
	// Point at a port with nothing listening.
	c, err := meilisearch.NewReadClient(config.Meili{
		URL:       "http://127.0.0.1:1",
		MasterKey: "k",
	})
	if err != nil {
		t.Fatalf("NewReadClient: %v", err)
	}
	if err := (meilisearch.Probe{Client: c}).Check(t.Context()); err == nil {
		t.Fatal("Check: want error on unreachable host")
	}
}
