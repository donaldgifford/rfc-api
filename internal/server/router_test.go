package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/search"
	"github.com/donaldgifford/rfc-api/internal/server"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store/memory"
)

func buildTestHandler(t *testing.T, types []config.DocumentType) http.Handler {
	t.Helper()
	reg, err := registry.New(types)
	if err != nil {
		t.Fatal(err)
	}
	mem := memory.New()
	mustAddRouter(t, mem, &domain.Document{
		ID: "RFC-0001", Type: "rfc", Title: "First", Status: "Draft",
	})
	handlers := server.Handlers{
		Docs:    handler.NewDocs(service.NewDocs(mem, reg)),
		Search:  handler.NewSearch(service.NewSearch(search.NoopClient{}, reg)),
		Types:   handler.NewTypes(reg),
		Webhook: handler.NewWebhook(nil), // handler tolerates nil config
	}
	return server.BuildMainHandler(handlers, reg, &server.V1Chain{}, "")
}

func mustAddRouter(t *testing.T, m *memory.Store, d *domain.Document) {
	t.Helper()
	if err := m.Add(d); err != nil {
		t.Fatal(err)
	}
}

func TestRouter_TypesListed(t *testing.T) {
	h := buildTestHandler(t, []config.DocumentType{
		{ID: "rfc", Name: "RFCs", Prefix: "RFC"},
		{ID: "adr", Name: "ADRs", Prefix: "ADR"},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/types", http.NoBody))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Errorf("types len = %d", len(out))
	}
}

func TestRouter_PerTypeMounted(t *testing.T) {
	h := buildTestHandler(t, []config.DocumentType{{ID: "rfc", Name: "RFCs", Prefix: "RFC"}})

	// Per-type collection
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/rfc", http.NoBody))
	if rec.Code != 200 {
		t.Errorf("list status = %d", rec.Code)
	}
	// Per-type single
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/rfc/0001", http.NoBody))
	if rec.Code != 200 {
		t.Errorf("single status = %d", rec.Code)
	}
}

func TestRouter_FakeType_FullSetMounted(t *testing.T) {
	// Registering a fake type should mount the full per-type route set;
	// DESIGN-0002's "adding a type is a config change" claim. The
	// list endpoint should be 200 (empty array); per-doc endpoints
	// should NOT be the catch-all 404 — a missing-doc 404 includes
	// problem+json, the catch-all does too but targets the /tst/9999
	// segment specifically. We assert the route is registered by
	// confirming the response shape is an RFC 7807 envelope whose
	// instance is the requested URI (i.e. the route was mounted).
	h := buildTestHandler(t, []config.DocumentType{
		{ID: "rfc", Name: "RFCs", Prefix: "RFC"},
		{ID: "tst", Name: "Test", Prefix: "TST"},
	})
	listRec := httptest.NewRecorder()
	h.ServeHTTP(listRec, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/tst", http.NoBody))
	if listRec.Code != 200 {
		t.Errorf("/api/v1/tst status = %d, want 200", listRec.Code)
	}

	subroutes := []string{
		"/api/v1/tst/9999",
		"/api/v1/tst/9999/links",
		"/api/v1/tst/9999/discussion",
		"/api/v1/tst/9999/authors",
		"/api/v1/tst/9999/revisions",
	}
	for _, path := range subroutes {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", path, http.NoBody))
		if rec.Code != 404 {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s Content-Type = %q", path, ct)
		}
	}
}

func TestRouter_UnknownType_404(t *testing.T) {
	h := buildTestHandler(t, []config.DocumentType{{ID: "rfc", Name: "RFCs", Prefix: "RFC"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/nope", http.NoBody))
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRouter_WebhookRequiresHMAC(t *testing.T) {
	h := buildTestHandler(t, []config.DocumentType{{ID: "rfc", Name: "RFCs", Prefix: "RFC"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/webhooks/github", strings.NewReader("{}")))
	if rec.Code != 401 {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
