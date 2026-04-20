// Package contract runs the full main-port handler against the
// hand-authored api/openapi.yaml and asserts every response matches
// its spec-declared schema. The test spins up BuildMainHandler in-
// process (not httptest.NewServer) and drives it through
// kin-openapi's request / response validator.
//
// Running this test on every PR is how we keep the spec and the
// server's actual behavior in sync — per DESIGN-0001 the spec is
// hand-authored, so we need a live check that claims match reality.
package contract_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/search"
	"github.com/donaldgifford/rfc-api/internal/server"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store/memory"
)

func loadSpec(t *testing.T) (*openapi3.T, routers.Router) {
	t.Helper()
	loader := openapi3.NewLoader()
	path := filepath.Join("..", "..", "api", "openapi.yaml")
	doc, err := loader.LoadFromFile(path)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("spec validation: %v", err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	return doc, router
}

func buildServer(t *testing.T) http.Handler {
	t.Helper()
	reg, err := registry.New([]config.DocumentType{
		{ID: "rfc", Name: "RFCs", Prefix: "RFC", Lifecycle: []string{"Draft", "Accepted"}},
		{ID: "adr", Name: "ADRs", Prefix: "ADR"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mem := memory.New()
	now := time.Now().UTC()
	if err := mem.Add(&domain.Document{
		ID: "RFC-0001", Type: "rfc", Title: "First", Status: "Draft",
		CreatedAt: now, UpdatedAt: now,
		Source: domain.Source{Repo: "x/x", Path: "docs/rfc/0001.md"},
	}); err != nil {
		t.Fatal(err)
	}
	handlers := server.Handlers{
		Docs:    handler.NewDocs(service.NewDocs(mem, reg)),
		Search:  handler.NewSearch(service.NewSearch(search.NoopClient{}, reg)),
		Types:   handler.NewTypes(reg),
		Webhook: handler.NewWebhook(slog.Default()),
	}
	return server.BuildMainHandler(handlers, reg, &server.V1Chain{}, "")
}

func validate(t *testing.T, router routers.Router, req *http.Request, res *http.Response, body []byte) {
	t.Helper()
	route, pathParams, err := router.FindRoute(req)
	if err != nil {
		t.Fatalf("spec has no route for %s %s: %v", req.Method, req.URL.Path, err)
	}
	reqInput := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    &openapi3filter.Options{MultiError: true},
	}
	if err := openapi3filter.ValidateRequest(context.Background(), reqInput); err != nil {
		t.Errorf("request validation failed: %v", err)
	}
	resInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqInput,
		Status:                 res.StatusCode,
		Header:                 res.Header,
		Options:                &openapi3filter.Options{MultiError: true},
	}
	resInput.SetBodyBytes(body)
	if err := openapi3filter.ValidateResponse(context.Background(), resInput); err != nil {
		t.Errorf("response validation failed: %v", err)
	}
}

func executeAndValidate(t *testing.T, h http.Handler, router routers.Router, method, url string) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, url, http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(body))

	validate(t, router, req, res, body)
}

func TestContract_HappyPathEndpoints(t *testing.T) {
	_, router := loadSpec(t)
	h := buildServer(t)

	cases := []struct {
		method, url string
	}{
		{http.MethodGet, "http://localhost/api/v1/types"},
		{http.MethodGet, "http://localhost/api/v1/docs"},
		{http.MethodGet, "http://localhost/api/v1/rfc"},
		{http.MethodGet, "http://localhost/api/v1/rfc/0001"},
		{http.MethodGet, "http://localhost/api/v1/rfc/0001/links"},
		{http.MethodGet, "http://localhost/api/v1/rfc/0001/discussion"},
		{http.MethodGet, "http://localhost/api/v1/rfc/0001/authors"},
		{http.MethodGet, "http://localhost/api/v1/rfc/0001/revisions"},
		{http.MethodGet, "http://localhost/api/v1/search?q=first"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.url, func(t *testing.T) {
			executeAndValidate(t, h, router, tc.method, tc.url)
		})
	}
}

func TestContract_NotFound_ProblemEnvelope(t *testing.T) {
	_, router := loadSpec(t)
	h := buildServer(t)
	executeAndValidate(t, h, router, http.MethodGet, "http://localhost/api/v1/rfc/9999")
}

func TestContract_BadRequest_ProblemEnvelope(t *testing.T) {
	_, router := loadSpec(t)
	h := buildServer(t)
	// Malformed cursor (invalid base64url JSON) triggers ErrInvalidInput
	// while staying within the spec's declared parameter constraints,
	// so request validation passes and we validate only the 400 body.
	executeAndValidate(t, h, router, http.MethodGet, "http://localhost/api/v1/rfc?cursor=not-a-real-cursor")
}
