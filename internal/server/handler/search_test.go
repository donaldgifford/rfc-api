package handler_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/search"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
	"github.com/donaldgifford/rfc-api/internal/service"
)

func newSearchHandler(t *testing.T) *handler.Search {
	t.Helper()
	reg, err := registry.New([]config.DocumentType{{ID: "rfc", Name: "RFCs", Prefix: "RFC"}})
	if err != nil {
		t.Fatal(err)
	}
	return handler.NewSearch(service.NewSearch(search.NoopClient{}, reg))
}

func TestSearchQuery_NoopEmpty(t *testing.T) {
	h := newSearchHandler(t)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/search?q=foo", nil)
	rec := httptest.NewRecorder()
	h.Query(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var hits []any
	if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("want 0 hits, got %d", len(hits))
	}
	if rec.Header().Get("X-Total-Count") != "0" {
		t.Errorf("X-Total-Count = %q", rec.Header().Get("X-Total-Count"))
	}
}

func TestSearchQuery_UnknownType(t *testing.T) {
	h := newSearchHandler(t)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/search?q=foo&type=nope", nil)
	rec := httptest.NewRecorder()
	h.Query(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSearchQuery_BadLimit(t *testing.T) {
	h := newSearchHandler(t)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/search?limit=-1", nil)
	rec := httptest.NewRecorder()
	h.Query(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
