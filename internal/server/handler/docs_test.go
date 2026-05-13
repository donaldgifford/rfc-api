package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// urlParse is a one-line alias so the import stays tucked in the
// header instead of leaking into every callsite.
var urlParse = url.Parse

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
	return handler.NewDocs(service.NewDocs(mem, reg), reg)
}

// newDocsHandlerMultiType seeds the memory store with one document
// per registered type so the filter / sort tests can exercise the
// OR-across-types and per-sort orderings without bleeding into the
// single-type happy-path fixture above.
func newDocsHandlerMultiType(t *testing.T) *handler.Docs {
	t.Helper()
	mem := memory.New()
	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	mustAdd(t, mem, &domain.Document{
		ID: "RFC-0001", Type: "rfc", Title: "RFC One",
		CreatedAt: now, UpdatedAt: now.Add(30 * time.Minute),
		Source: domain.Source{Repo: "x/x", Path: "docs/rfc/0001.md"},
	})
	mustAdd(t, mem, &domain.Document{
		ID: "ADR-0001", Type: "adr", Title: "ADR One",
		CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour),
		Source: domain.Source{Repo: "x/x", Path: "docs/adr/0001.md"},
	})
	mustAdd(t, mem, &domain.Document{
		ID: "DESIGN-0001", Type: "design", Title: "Design One",
		CreatedAt: now.Add(2 * time.Hour), UpdatedAt: now,
		Source: domain.Source{Repo: "x/x", Path: "docs/design/0001.md"},
	})
	reg, err := registry.New([]config.DocumentType{
		{ID: "rfc", Name: "RFCs", Prefix: "RFC"},
		{ID: "adr", Name: "ADRs", Prefix: "ADR"},
		{ID: "design", Name: "Designs", Prefix: "DESIGN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler.NewDocs(service.NewDocs(mem, reg), reg)
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

// --- IMPL-0007 Phase 3: ?filter= and ?sort= on /api/v1/docs ---

// TestDocsListAll_FilterOnly_HappyPath pins the OR-within-type
// semantics from DESIGN-0003 #Filter-semantics. Asking for rfc + adr
// returns rfc + adr documents and excludes design. X-Total-Count is
// the filtered total (2) and X-Total-Count-Unfiltered is the full
// corpus (3) since a filter is active.
func TestDocsListAll_FilterOnly_HappyPath(t *testing.T) {
	h := newDocsHandlerMultiType(t)
	req := httptest.NewRequestWithContext(
		t.Context(), "GET",
		"/api/v1/docs?filter=type:rfc&filter=type:adr",
		http.NoBody,
	)
	rec := httptest.NewRecorder()
	h.ListAll(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Total-Count"); got != "2" {
		t.Errorf("X-Total-Count = %q, want 2", got)
	}
	if got := rec.Header().Get("X-Total-Count-Unfiltered"); got != "3" {
		t.Errorf("X-Total-Count-Unfiltered = %q, want 3 (full corpus)", got)
	}
	var out []domain.Document
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, d := range out {
		if d.Type == "design" {
			t.Errorf("design doc %q leaked into rfc+adr filter result", d.ID)
		}
	}
}

// TestDocsListAll_SortOnly_HappyPath pins that ?sort= changes the
// ordering without affecting the result set. updated_desc against
// the multi-type fixture orders by updated_at: ADR-0001
// (most-recently updated), RFC-0001 (30m), DESIGN-0001 (4h ago).
func TestDocsListAll_SortOnly_HappyPath(t *testing.T) {
	h := newDocsHandlerMultiType(t)
	req := httptest.NewRequestWithContext(
		t.Context(), "GET",
		"/api/v1/docs?sort=updated_desc",
		http.NoBody,
	)
	rec := httptest.NewRecorder()
	h.ListAll(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// No filter = no X-Total-Count-Unfiltered header (zero visible
	// change for unfiltered callers, per DESIGN-0003).
	if got := rec.Header().Get("X-Total-Count-Unfiltered"); got != "" {
		t.Errorf("X-Total-Count-Unfiltered = %q, want empty (no filter active)", got)
	}
	var out []domain.Document
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := []domain.DocumentID{"ADR-0001", "RFC-0001", "DESIGN-0001"}
	if len(out) != len(want) {
		t.Fatalf("len = %d, want %d", len(out), len(want))
	}
	for i := range want {
		if out[i].ID != want[i] {
			t.Errorf("position %d: got %q, want %q (full: %v)", i, out[i].ID, want[i], docIDs(out))
		}
	}
}

// TestDocsListAll_NoParams_NoUnfilteredHeader is the no-op
// regression guard: a parameter-free request must produce byte-
// identical headers to today's behavior. Only X-Total-Count is set;
// X-Total-Count-Unfiltered must not appear.
func TestDocsListAll_NoParams_NoUnfilteredHeader(t *testing.T) {
	h := newDocsHandlerMultiType(t)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/docs", http.NoBody)
	rec := httptest.NewRecorder()
	h.ListAll(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Total-Count"); got != "3" {
		t.Errorf("X-Total-Count = %q, want 3", got)
	}
	if got := rec.Header().Get("X-Total-Count-Unfiltered"); got != "" {
		t.Errorf("X-Total-Count-Unfiltered = %q, want empty", got)
	}
}

// TestDocsListAll_FilterPlusSort_CursorRoundTrip drives the
// happy-path round trip the DirectoryToolbar will hit in
// production: filter narrows the result set, sort orders it,
// cursor paginates inside that view. Page-1 → use Link rel=next →
// page-2 stays inside the filtered+sorted view.
func TestDocsListAll_FilterPlusSort_CursorRoundTrip(t *testing.T) {
	h := newDocsHandlerMultiType(t)
	// Page 1: filter to rfc+adr (excludes design), sort by id_asc,
	// limit=1 so we get exactly one row + a cursor.
	req := httptest.NewRequestWithContext(
		t.Context(), "GET",
		"/api/v1/docs?filter=type:rfc&filter=type:adr&sort=id_asc&limit=1",
		http.NoBody,
	)
	rec := httptest.NewRecorder()
	h.ListAll(rec, req)
	if rec.Code != 200 {
		t.Fatalf("page 1 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var page1 []domain.Document
	if err := json.Unmarshal(rec.Body.Bytes(), &page1); err != nil {
		t.Fatal(err)
	}
	if len(page1) != 1 || page1[0].ID != "ADR-0001" {
		t.Fatalf("page 1 = %v, want [ADR-0001]", docIDs(page1))
	}
	link := rec.Header().Get("Link")
	if link == "" {
		t.Fatalf("page 1 Link header missing")
	}
	// Link header must preserve filter + sort across pagination
	// (DESIGN-0003 #Cursor-pagination-and-link-preservation).
	for _, want := range []string{"filter=type%3Arfc", "filter=type%3Aadr", "sort=id_asc"} {
		if !containsSubstring(link, want) {
			t.Errorf("Link header missing %q: %q", want, link)
		}
	}
	// Pull the next-page cursor out of the Link header and follow
	// it with a fresh request that mirrors what a client would do.
	nextURL := extractRelNextURL(t, link)
	req2 := httptest.NewRequestWithContext(t.Context(), "GET", nextURL, http.NoBody)
	rec2 := httptest.NewRecorder()
	h.ListAll(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("page 2 status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	var page2 []domain.Document
	if err := json.Unmarshal(rec2.Body.Bytes(), &page2); err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].ID != "RFC-0001" {
		t.Errorf("page 2 = %v, want [RFC-0001]", docIDs(page2))
	}
}

// TestDocsListAll_BadInputs_All400 covers every documented
// malformed-input failure mode. Each case must return 400 +
// application/problem+json (DESIGN-0003 #Error-contract).
func TestDocsListAll_BadInputs_All400(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"unknown_filter_field", "/api/v1/docs?filter=status:accepted"},
		{"malformed_filter", "/api/v1/docs?filter=novalue"},
		{"unknown_type_value", "/api/v1/docs?filter=type:notreal"},
		{"unknown_sort_value", "/api/v1/docs?sort=weird"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newDocsHandlerMultiType(t)
			req := httptest.NewRequestWithContext(t.Context(), "GET", c.url, http.NoBody)
			rec := httptest.NewRecorder()
			h.ListAll(rec, req)
			if rec.Code != 400 {
				t.Errorf("status = %d, body = %s, want 400", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}
		})
	}
}

// TestDocsListAll_CursorSortMismatch_400 is the cross-check seam: a
// cursor minted under sort A presented alongside ?sort=B must 400
// rather than silently re-keying the keyset against the wrong
// column.
func TestDocsListAll_CursorSortMismatch_400(t *testing.T) {
	h := newDocsHandlerMultiType(t)
	// Get a real cursor by paginating under id_asc.
	req1 := httptest.NewRequestWithContext(
		t.Context(), "GET",
		"/api/v1/docs?sort=id_asc&limit=1",
		http.NoBody,
	)
	rec1 := httptest.NewRecorder()
	h.ListAll(rec1, req1)
	if rec1.Code != 200 {
		t.Fatalf("seed page status = %d", rec1.Code)
	}
	nextURL := extractRelNextURL(t, rec1.Header().Get("Link"))

	// Same cursor but flip sort to updated_desc — the cursor's S
	// field carries id_asc and the request asks for updated_desc.
	mismatchURL := swapSort(nextURL, "updated_desc")
	req2 := httptest.NewRequestWithContext(t.Context(), "GET", mismatchURL, http.NoBody)
	rec2 := httptest.NewRecorder()
	h.ListAll(rec2, req2)
	if rec2.Code != 400 {
		t.Errorf("status = %d, body = %s, want 400 (cursor sort mismatch)", rec2.Code, rec2.Body.String())
	}
	if ct := rec2.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
}

// docIDs is a small projection helper used only in this test file
// so that order-asserting failures have readable diffs.
func docIDs(docs []domain.Document) []domain.DocumentID {
	out := make([]domain.DocumentID, len(docs))
	for i := range docs {
		out[i] = docs[i].ID
	}
	return out
}

// containsSubstring keeps the test file dependency-free; the
// stdlib `strings.Contains` would do the same job but introduces a
// new import in a file that has none.
func containsSubstring(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// extractRelNextURL pulls the URL out of a single-link Link header
// of the form `<url>; rel="next"` — the only shape ArrayJSON emits
// for a list page with a NextCursor and no PrevCursor.
func extractRelNextURL(t *testing.T, link string) string {
	t.Helper()
	if link == "" {
		t.Fatalf("Link header empty")
	}
	start := -1
	for i := 0; i < len(link); i++ {
		if link[i] == '<' {
			start = i + 1
			break
		}
	}
	end := -1
	for i := start; i < len(link); i++ {
		if link[i] == '>' {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatalf("Link header missing <…>: %q", link)
	}
	return link[start:end]
}

// swapSort rewrites the `sort=` value in a URL. Used to force a
// cursor-sort mismatch from a real next-page URL.
func swapSort(rawURL, newSort string) string {
	u, err := urlParse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("sort", newSort)
	u.RawQuery = q.Encode()
	return u.String()
}
