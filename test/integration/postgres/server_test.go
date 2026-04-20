//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/search"
	"github.com/donaldgifford/rfc-api/internal/server"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
	"github.com/donaldgifford/rfc-api/internal/server/middleware"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store/postgres"
)

// spinUpServer returns an httptest.Server backed by the real Postgres
// store. The caller is responsible for seeding documents into `pool`
// before issuing requests.
func spinUpServer(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()

	reg, err := registry.New([]config.DocumentType{
		{ID: "rfc", Name: "RFCs", Prefix: "RFC"},
		{ID: "adr", Name: "ADRs", Prefix: "ADR"},
	})
	if err != nil {
		t.Fatal(err)
	}

	pgStore := postgres.NewDocs(pool)
	handlers := server.Handlers{
		Docs:    handler.NewDocs(service.NewDocs(pgStore, reg)),
		Search:  handler.NewSearch(service.NewSearch(search.NoopClient{}, reg)),
		Types:   handler.NewTypes(reg),
		Webhook: handler.NewWebhook(slog.Default()),
	}

	v1 := server.V1Chain{CORS: middleware.DefaultCORS([]string{"*"})}
	h := server.BuildMainHandler(handlers, reg, &v1, "integration-test-secret")

	root := middleware.Chain(
		middleware.OTel(noop.NewTracerProvider()),
		middleware.Recover,
		middleware.RequestID,
		middleware.Logger,
	)
	return httptest.NewServer(root(h))
}

func seedDoc(t *testing.T, pool *pgxpool.Pool, id, typeID string, created time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO documents (id, type, title, status, body, created_at, updated_at,
		                       source_repo, source_path, source_commit)
		VALUES ($1, $2, $3, 'Draft', '', $4, $4, 'x/x', '', '')`,
		id, typeID, "Title "+id, created)
	if err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func TestServer_GetDoc_RoundtripsThroughPostgres(t *testing.T) {
	pool := testPool(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedDoc(t, pool, "RFC-0001", "rfc", now)

	srv := spinUpServer(t, pool)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+"/api/v1/rfc/0001", http.NoBody)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}

	var doc domain.Document
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != "RFC-0001" {
		t.Errorf("doc.ID = %q, want RFC-0001", doc.ID)
	}
	if doc.Type != "rfc" {
		t.Errorf("doc.Type = %q, want rfc", doc.Type)
	}
}

func TestServer_GetMissing_Returns_404_ProblemJSON(t *testing.T) {
	pool := testPool(t)
	srv := spinUpServer(t, pool)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+"/api/v1/rfc/9999", http.NoBody)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
}

func TestServer_ListByType_ReturnsBareArrayWithHeaders(t *testing.T) {
	pool := testPool(t)

	base := time.Now().UTC().Truncate(time.Microsecond)
	seedDoc(t, pool, "RFC-0001", "rfc", base.Add(-2*time.Hour))
	seedDoc(t, pool, "RFC-0002", "rfc", base.Add(-1*time.Hour))
	seedDoc(t, pool, "RFC-0003", "rfc", base)
	seedDoc(t, pool, "ADR-0001", "adr", base.Add(-30*time.Minute))

	srv := spinUpServer(t, pool)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+"/api/v1/rfc?limit=2", http.NoBody)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if res.Header.Get("X-Total-Count") != "3" {
		t.Errorf("X-Total-Count = %q, want 3", res.Header.Get("X-Total-Count"))
	}
	if link := res.Header.Get("Link"); !strings.Contains(link, `rel="next"`) {
		t.Errorf("Link header missing next: %q", link)
	}

	var items []domain.Document
	if err := json.NewDecoder(res.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	// Newest first per (created_at DESC, id ASC).
	if items[0].ID != "RFC-0003" {
		t.Errorf("items[0] = %q, want RFC-0003", items[0].ID)
	}
	if items[1].ID != "RFC-0002" {
		t.Errorf("items[1] = %q, want RFC-0002", items[1].ID)
	}
}

func TestServer_ListAll_CrossType(t *testing.T) {
	pool := testPool(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedDoc(t, pool, "RFC-0001", "rfc", now.Add(-time.Hour))
	seedDoc(t, pool, "ADR-0001", "adr", now)

	srv := spinUpServer(t, pool)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+"/api/v1/docs?limit=10", http.NoBody)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if res.Header.Get("X-Total-Count") != "2" {
		t.Errorf("X-Total-Count = %q, want 2", res.Header.Get("X-Total-Count"))
	}
}

func TestServer_TypesEndpoint_LiesOverRegistry(t *testing.T) {
	pool := testPool(t)
	srv := spinUpServer(t, pool)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+"/api/v1/types", http.NoBody)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
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
	if len(types) != 2 {
		t.Errorf("len(types) = %d, want 2 (rfc, adr)", len(types))
	}
}
