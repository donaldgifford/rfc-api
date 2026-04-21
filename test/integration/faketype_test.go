// Package faketype_test graduates DESIGN-0002's "adding a type is
// a config change, not a code change" claim from the route-mounting
// test in internal/server/router_test.go to a full parse → persist →
// serve round-trip. If this test passes for a never-before-seen type
// `tst` wired only through the document-type registry + parser
// registry, DESIGN-0002's invariant holds.
//
// Not gated by the integration build tag — the in-memory store
// suffices. The Postgres round-trip is already covered by
// test/integration/postgres/.
package faketype_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/parser"
	_ "github.com/donaldgifford/rfc-api/internal/parser/testparser" // register test-parser
	"github.com/donaldgifford/rfc-api/internal/search"
	"github.com/donaldgifford/rfc-api/internal/server"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store/memory"
)

// tstType is the contrived type the test spins up. Never registered
// in production code — this is the whole point of the harness.
var tstType = config.DocumentType{
	ID:        "tst",
	Name:      "Tests",
	Prefix:    "TST",
	Lifecycle: []string{"Draft", "Proposed", "Accepted"},
}

// fakeTypeFixture is a single YAML document the test-parser chews.
const fakeTypeFixture = `id: TST-0001
title: "First Fake"
status: Draft
authors:
  - name: Alice
    handle: alice
labels: [demo, harness]
body: "body prose"
extensions:
  priority: high
`

// setup builds the full HTTP stack for the tst type: registry + in-
// memory store + parser + server. Returns an httptest.Server the
// caller issues requests against.
func setup(t *testing.T) *httptest.Server {
	t.Helper()

	reg, err := registry.New([]config.DocumentType{tstType})
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	p, err := parser.Default.Get("test-parser")
	if err != nil {
		t.Fatalf("lookup test-parser: %v", err)
	}

	dt, _ := reg.Get("tst")
	doc, err := p.Parse([]byte(fakeTypeFixture), dt, domain.Source{
		Repo: "owner/repo", Path: "tst/0001.yaml", Commit: "sha0001",
	})
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	mem := memory.New()
	if err := mem.Add(&doc); err != nil {
		t.Fatalf("seed: %v", err)
	}

	handlers := server.Handlers{
		Docs:    handler.NewDocs(service.NewDocs(mem, reg)),
		Search:  handler.NewSearch(service.NewSearch(search.NoopClient{}, reg)),
		Types:   handler.NewTypes(reg),
		Webhook: handler.NewWebhook(&handler.WebhookConfig{Logger: slog.Default()}),
	}
	h := server.BuildMainHandler(handlers, reg, &server.V1Chain{}, "test-secret")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, http.NoBody)
	if err != nil {
		t.Fatalf("build %s: %v", path, err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return res
}

func TestFakeType_GetDoc(t *testing.T) {
	srv := setup(t)

	res := get(t, srv, "/api/v1/tst/0001")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d: %s", res.StatusCode, body)
	}

	var doc domain.Document
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != "TST-0001" || doc.Type != "tst" || doc.Title != "First Fake" {
		t.Errorf("doc shape wrong: %+v", doc)
	}
}

func TestFakeType_ListMounted(t *testing.T) {
	srv := setup(t)

	res := get(t, srv, "/api/v1/tst")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var items []domain.Document
	if err := json.NewDecoder(res.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "TST-0001" {
		t.Errorf("items = %+v", items)
	}
}

func TestFakeType_AuthorsEndpoint(t *testing.T) {
	srv := setup(t)
	res := get(t, srv, "/api/v1/tst/0001/authors")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var authors []domain.Author
	if err := json.NewDecoder(res.Body).Decode(&authors); err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || authors[0].Handle != "alice" {
		t.Errorf("authors = %+v", authors)
	}
}

func TestFakeType_LinksEndpoint(t *testing.T) {
	srv := setup(t)
	res := get(t, srv, "/api/v1/tst/0001/links")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	// Body has no cross-refs — test-parser does no link extraction —
	// so this should be a bare [] with total-count 0.
	if res.Header.Get("Content-Type") == "application/problem+json" {
		t.Errorf("links endpoint returned problem+json unexpectedly")
	}
}

func TestFakeType_TypesEndpoint_ListsTst(t *testing.T) {
	srv := setup(t)
	res := get(t, srv, "/api/v1/types")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var types []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&types); err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 || types[0].ID != "tst" {
		t.Errorf("types = %+v, want just tst", types)
	}
}

func TestFakeType_LifecycleViolation_Returns400(t *testing.T) {
	// A fresh parse with an out-of-lifecycle status must fail before
	// anything hits the store.
	reg, _ := registry.New([]config.DocumentType{tstType})
	dt, _ := reg.Get("tst")
	p, _ := parser.Default.Get("test-parser")

	bad := []byte(`id: TST-0002
title: Bad
status: NotInLifecycle
`)
	_, err := p.Parse(bad, dt, domain.Source{})
	if err == nil {
		t.Fatal("want lifecycle-violation error")
	}
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput (for httperr 400)", err)
	}
}
