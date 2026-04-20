package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
	"github.com/donaldgifford/rfc-api/internal/server/routectx"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store/memory"
)

func newDocsHandler(t *testing.T) *handler.Docs {
	t.Helper()
	mem := memory.New()
	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	mustAdd(t, mem, &domain.Document{
		ID: "RFC-0001", Type: "rfc", Title: "First", Status: "Draft",
		CreatedAt: now, UpdatedAt: now,
		Authors: []domain.Author{{Name: "Ada"}},
		Source:  domain.Source{Repo: "x/x", Path: "docs/rfc/0001.md"},
	})
	mustAdd(t, mem, &domain.Document{
		ID: "RFC-0002", Type: "rfc", Title: "Second", Status: "Proposed",
		CreatedAt: now.Add(time.Hour), UpdatedAt: now,
		Source: domain.Source{Repo: "x/x", Path: "docs/rfc/0002.md"},
	})
	reg, err := registry.New([]config.DocumentType{{ID: "rfc", Name: "RFCs", Prefix: "RFC"}})
	if err != nil {
		t.Fatal(err)
	}
	return handler.NewDocs(service.NewDocs(mem, reg))
}

func mustAdd(t *testing.T, m *memory.Store, d *domain.Document) {
	t.Helper()
	if err := m.Add(d); err != nil {
		t.Fatal(err)
	}
}

func requestWithRoute(typeID, pattern, urlPath, pathValueID string) *http.Request {
	base := httptest.NewRequestWithContext(context.Background(), "GET", urlPath, http.NoBody)
	req := base.WithContext(routectx.With(base.Context(), typeID, pattern))
	if pathValueID != "" {
		req.SetPathValue("id", pathValueID)
	}
	return req
}

func TestDocsGet_Happy(t *testing.T) {
	h := newDocsHandler(t)
	req := requestWithRoute("rfc", "/api/v1/rfc/{id}", "/api/v1/rfc/0001", "0001")
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var doc domain.Document
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != "RFC-0001" {
		t.Errorf("doc.ID = %q", doc.ID)
	}
}

func TestDocsGet_NotFound(t *testing.T) {
	h := newDocsHandler(t)
	req := requestWithRoute("rfc", "/api/v1/rfc/{id}", "/api/v1/rfc/9999", "9999")
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestDocsListByType_Paginated(t *testing.T) {
	h := newDocsHandler(t)
	req := requestWithRoute("rfc", "/api/v1/rfc", "/api/v1/rfc?limit=1", "")
	rec := httptest.NewRecorder()
	h.ListByType(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("X-Total-Count") != "2" {
		t.Errorf("X-Total-Count = %q", rec.Header().Get("X-Total-Count"))
	}
	if rec.Header().Get("Link") == "" {
		t.Error("expected Link header for paginated response")
	}
	var out []domain.Document
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Errorf("items len = %d, want 1", len(out))
	}
}

func TestDocsListByType_BadLimit(t *testing.T) {
	h := newDocsHandler(t)
	req := requestWithRoute("rfc", "/api/v1/rfc", "/api/v1/rfc?limit=9999", "")
	rec := httptest.NewRecorder()
	h.ListByType(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDocsListByType_BadCursor(t *testing.T) {
	h := newDocsHandler(t)
	req := requestWithRoute("rfc", "/api/v1/rfc", "/api/v1/rfc?cursor=!!!", "")
	rec := httptest.NewRecorder()
	h.ListByType(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDocsListByType_UnknownType(t *testing.T) {
	h := newDocsHandler(t)
	req := requestWithRoute("nope", "/api/v1/nope", "/api/v1/nope", "")
	rec := httptest.NewRecorder()
	h.ListByType(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDocsListAll(t *testing.T) {
	h := newDocsHandler(t)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/docs", nil)
	rec := httptest.NewRecorder()
	h.ListAll(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("X-Total-Count") != "2" {
		t.Errorf("X-Total-Count = %q", rec.Header().Get("X-Total-Count"))
	}
}

func TestDocsAuthors(t *testing.T) {
	h := newDocsHandler(t)
	req := requestWithRoute("rfc", "/api/v1/rfc/{id}/authors", "/api/v1/rfc/0001/authors", "0001")
	rec := httptest.NewRecorder()
	h.Authors(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []domain.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Name != "Ada" {
		t.Errorf("authors = %+v", out)
	}
}

func TestDocsDiscussion(t *testing.T) {
	h := newDocsHandler(t)
	req := requestWithRoute("rfc", "/api/v1/rfc/{id}/discussion", "/api/v1/rfc/0001/discussion", "0001")
	rec := httptest.NewRecorder()
	h.Discussion(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestDocsLinks(t *testing.T) {
	h := newDocsHandler(t)
	req := requestWithRoute("rfc", "/api/v1/rfc/{id}/links", "/api/v1/rfc/0001/links", "0001")
	rec := httptest.NewRecorder()
	h.Links(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestDocsRevisions_Stub(t *testing.T) {
	h := newDocsHandler(t)
	req := requestWithRoute("rfc", "/api/v1/rfc/{id}/revisions", "/api/v1/rfc/0001/revisions", "0001")
	rec := httptest.NewRecorder()
	h.Revisions(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}
