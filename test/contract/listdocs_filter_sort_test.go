// IMPL-0007 Phase 4: contract coverage for the listDocs filter +
// sort parameters introduced by DESIGN-0003. Drives the in-process
// handler through the live api/openapi.yaml via kin-openapi, the
// same seam contract_test.go uses for the baseline endpoints.
//
// The goal here is not to re-exercise sort dispatch (the handler
// tests already cover that) but to pin that the spec and the live
// server agree on:
//   - parameter shapes (filter[], sort enum)
//   - response shape under each variant (200 + DocumentListFilterable)
//   - error envelope for every documented 400 case
//   - X-Total-Count-Unfiltered behavior (present only when filter is
//     active — DESIGN-0003 #Total-count-headers)
package contract_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/search"
	"github.com/donaldgifford/rfc-api/internal/server"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store/memory"
)

// buildFilterSortServer seeds three docs across three types so the
// filter / sort permutations have observably different result sets
// and orders. The single-doc fixture in contract_test.go is fine
// for happy-path schema validation but can't distinguish "rfc-only
// filter" from "no filter" or "id_asc" from "created_desc".
func buildFilterSortServer(t *testing.T) http.Handler {
	t.Helper()
	reg, err := registry.New([]config.DocumentType{
		{ID: "rfc", Name: "RFCs", Prefix: "RFC", Lifecycle: []string{"Draft", "Accepted"}},
		{ID: "adr", Name: "ADRs", Prefix: "ADR"},
		{ID: "design", Name: "Designs", Prefix: "DESIGN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mem := memory.New()
	base := time.Now().UTC().Truncate(time.Microsecond)
	seeds := []*domain.Document{
		{
			ID: "RFC-0001", Type: "rfc", Title: "RFC One", Status: "Draft",
			CreatedAt: base, UpdatedAt: base.Add(30 * time.Minute),
			Source: domain.Source{Repo: "x/x", Path: "docs/rfc/0001.md"},
		},
		{
			ID: "ADR-0001", Type: "adr", Title: "ADR One",
			CreatedAt: base.Add(time.Hour), UpdatedAt: base.Add(time.Hour),
			Source: domain.Source{Repo: "x/x", Path: "docs/adr/0001.md"},
		},
		{
			ID: "DESIGN-0001", Type: "design", Title: "Design One",
			CreatedAt: base.Add(2 * time.Hour), UpdatedAt: base,
			Source: domain.Source{Repo: "x/x", Path: "docs/design/0001.md"},
		},
	}
	for _, d := range seeds {
		if err := mem.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	handlers := server.Handlers{
		Docs:    handler.NewDocs(service.NewDocs(mem, reg), reg),
		Search:  handler.NewSearch(service.NewSearch(search.NoopClient{}, reg)),
		Types:   handler.NewTypes(reg),
		Webhook: handler.NewWebhook(&handler.WebhookConfig{Logger: slog.Default()}),
	}
	return server.BuildMainHandler(handlers, reg, &server.V1Chain{}, "")
}

// execAndDecode runs an in-process request, validates against the
// spec, and returns the parsed array body + the captured response.
// Splitting this out from executeAndValidate so the tests below can
// inspect headers and the decoded body in a single helper.
func execAndDecode(
	t *testing.T,
	h http.Handler,
	router routers.Router,
	rawURL string,
) (http.Header, []domain.Document) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(body))

	validate(t, router, req, res, body)

	var out []domain.Document
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode body: %v (body=%s)", err, body)
		}
	}
	return res.Header, out
}

// TestContract_ListDocs_FilterOnly_SubsetOfBaseline pins that an
// active filter narrows the result set without producing rows
// outside the documented union — and that the X-Total-Count-
// Unfiltered header lights up exactly when a filter is active.
func TestContract_ListDocs_FilterOnly_SubsetOfBaseline(t *testing.T) {
	_, router := loadSpec(t)
	h := buildFilterSortServer(t)

	// Baseline: no params. Unfiltered header must be absent.
	baseHeaders, baseDocs := execAndDecode(t, h, router, "http://localhost/api/v1/docs")
	if got := baseHeaders.Get("X-Total-Count-Unfiltered"); got != "" {
		t.Errorf("baseline X-Total-Count-Unfiltered = %q, want empty", got)
	}
	if len(baseDocs) != 3 {
		t.Fatalf("baseline len = %d, want 3", len(baseDocs))
	}

	// Filter: rfc + adr only. Filtered subset; unfiltered header
	// must equal the baseline total.
	headers, docs := execAndDecode(
		t, h, router,
		"http://localhost/api/v1/docs?filter=type:rfc&filter=type:adr",
	)
	if got := headers.Get("X-Total-Count"); got != "2" {
		t.Errorf("X-Total-Count = %q, want 2", got)
	}
	if got := headers.Get("X-Total-Count-Unfiltered"); got != "3" {
		t.Errorf("X-Total-Count-Unfiltered = %q, want 3", got)
	}
	if len(docs) != 2 {
		t.Fatalf("filtered len = %d, want 2", len(docs))
	}
	for _, d := range docs {
		if d.Type != "rfc" && d.Type != "adr" {
			t.Errorf("type %q leaked into rfc+adr filter result", d.Type)
		}
	}
}

// TestContract_ListDocs_SortOnly_ReordersBaseline confirms that
// ?sort= preserves the result-set size and contents while reordering
// the array. With three docs created at distinct timestamps,
// `created_asc` should reverse the default `created_desc` order.
func TestContract_ListDocs_SortOnly_ReordersBaseline(t *testing.T) {
	_, router := loadSpec(t)
	h := buildFilterSortServer(t)

	_, baseline := execAndDecode(t, h, router, "http://localhost/api/v1/docs")
	_, ascending := execAndDecode(t, h, router, "http://localhost/api/v1/docs?sort=created_asc")
	if len(baseline) != len(ascending) {
		t.Fatalf("len mismatch: baseline=%d, asc=%d", len(baseline), len(ascending))
	}
	// Same multiset, reversed order.
	for i := range baseline {
		if baseline[i].ID != ascending[len(ascending)-1-i].ID {
			t.Errorf(
				"position %d: baseline=%q vs asc[reverse]=%q",
				i, baseline[i].ID, ascending[len(ascending)-1-i].ID,
			)
		}
	}
}

// TestContract_ListDocs_FilterSortCursor_RoundTrip exercises the
// full DirectoryToolbar shape end-to-end: filter narrows, sort
// orders, limit=1 forces a Link header, and the next URL must keep
// the caller inside the same filtered + sorted view.
func TestContract_ListDocs_FilterSortCursor_RoundTrip(t *testing.T) {
	_, router := loadSpec(t)
	h := buildFilterSortServer(t)

	page1Headers, page1 := execAndDecode(
		t, h, router,
		"http://localhost/api/v1/docs?filter=type:rfc&filter=type:adr&sort=id_asc&limit=1",
	)
	if len(page1) != 1 || page1[0].ID != "ADR-0001" {
		t.Fatalf("page1 = %v, want [ADR-0001]", idsOf(page1))
	}
	link := page1Headers.Get("Link")
	for _, want := range []string{"filter=type%3Arfc", "filter=type%3Aadr", "sort=id_asc"} {
		if !strings.Contains(link, want) {
			t.Errorf("Link header missing %q: %q", want, link)
		}
	}
	nextURL := relNextURL(t, link)
	// The relative Link URL needs the same scheme+host the test
	// server is using to feed back through ServeHTTP / kin-openapi.
	if !strings.HasPrefix(nextURL, "http") {
		nextURL = "http://localhost" + nextURL
	}
	_, page2 := execAndDecode(t, h, router, nextURL)
	if len(page2) != 1 || page2[0].ID != "RFC-0001" {
		t.Errorf("page2 = %v, want [RFC-0001]", idsOf(page2))
	}
}

// TestContract_ListDocs_BadInputs_ProblemEnvelope confirms every
// documented 400 case validates against the
// ProblemBadRequest response in the spec. kin-openapi sees these
// as well-formed `?filter=type:foo` / `?sort=foo` strings (they
// match the parameter pattern + enum) and a 400 with the right
// body, which is the desired behavior — the spec says 400 + problem
// envelope is a legitimate response.
//
// Cases that violate the spec's *parameter* pattern (e.g.
// `filter=novalue` which fails the regex) are deliberately *not*
// in this table — kin-openapi rejects those at the request-validate
// stage rather than letting the handler respond, and that fast-fail
// is correct behavior. Those are pinned by handler unit tests.
func TestContract_ListDocs_BadInputs_ProblemEnvelope(t *testing.T) {
	_, router := loadSpec(t)
	h := buildFilterSortServer(t)
	cases := []struct {
		name string
		url  string
	}{
		{"unknown_filter_field", "http://localhost/api/v1/docs?filter=status:accepted"},
		{"unknown_type_value", "http://localhost/api/v1/docs?filter=type:notreal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, c.url, http.NoBody)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			res := rec.Result()
			body, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			res.Body = io.NopCloser(bytes.NewReader(body))

			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%s)", res.StatusCode, body)
			}
			if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}
			// Validate only the response shape: the spec's parameter
			// schema would otherwise reject the request because
			// `status:accepted` matches the documented filter regex
			// but the handler reports it as an unsupported field —
			// that's the contract we want to pin.
			route, pathParams, err := router.FindRoute(req)
			if err != nil {
				t.Fatalf("router: %v", err)
			}
			resInput := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{
					Request: req, PathParams: pathParams, Route: route,
				},
				Status: res.StatusCode, Header: res.Header,
				Options: &openapi3filter.Options{MultiError: true},
			}
			resInput.SetBodyBytes(body)
			if err := openapi3filter.ValidateResponse(context.Background(), resInput); err != nil {
				t.Errorf("response validation failed: %v", err)
			}
		})
	}
}

// idsOf is a small projection helper, kept local to this file so
// the assertion failures stay readable without bloating the
// contract_test.go helper set.
func idsOf(docs []domain.Document) []domain.DocumentID {
	out := make([]domain.DocumentID, len(docs))
	for i := range docs {
		out[i] = docs[i].ID
	}
	return out
}

// relNextURL pulls the rel="next" URL out of a Link header of the
// form `<url>; rel="next"`. ArrayJSON only emits a single rel value
// when there is no prev cursor, so the parse stays minimal.
func relNextURL(t *testing.T, link string) string {
	t.Helper()
	if link == "" {
		t.Fatalf("Link header empty")
	}
	start := strings.IndexByte(link, '<')
	end := strings.IndexByte(link, '>')
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("malformed Link header: %q", link)
	}
	u, err := url.Parse(link[start+1 : end])
	if err != nil {
		t.Fatalf("parse Link URL: %v", err)
	}
	return u.String()
}
