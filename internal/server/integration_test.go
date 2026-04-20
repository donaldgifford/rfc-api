package server_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/search"
	"github.com/donaldgifford/rfc-api/internal/server"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
	"github.com/donaldgifford/rfc-api/internal/server/middleware"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store/memory"
)

const webhookSecret = "integration-test-secret"

func spinUp(t *testing.T, opts ...func(*V1Override)) *httptest.Server {
	t.Helper()
	reg, err := registry.New([]config.DocumentType{
		{ID: "rfc", Name: "RFCs", Prefix: "RFC"},
		{ID: "adr", Name: "ADRs", Prefix: "ADR"},
	})
	if err != nil {
		t.Fatal(err)
	}

	mem := memory.New()
	now := time.Now().UTC()
	seeds := []*domain.Document{
		{ID: "RFC-0001", Type: "rfc", Title: "First", Status: "Draft", CreatedAt: now, UpdatedAt: now},
		{ID: "RFC-0002", Type: "rfc", Title: "Second", Status: "Proposed", CreatedAt: now.Add(time.Minute), UpdatedAt: now},
		{ID: "ADR-0001", Type: "adr", Title: "An ADR", Status: "Accepted", CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now},
	}
	for _, doc := range seeds {
		if err := mem.Add(doc); err != nil {
			t.Fatal(err)
		}
	}

	handlers := server.Handlers{
		Docs:    handler.NewDocs(service.NewDocs(mem, reg)),
		Search:  handler.NewSearch(service.NewSearch(search.NoopClient{}, reg)),
		Types:   handler.NewTypes(reg),
		Webhook: handler.NewWebhook(slog.Default()),
	}

	override := V1Override{
		CORSOrigins: []string{"https://rfc-site.example"},
	}
	for _, opt := range opts {
		opt(&override)
	}
	v1 := server.V1Chain{
		Timeout:   override.Timeout,
		CORS:      middleware.DefaultCORS(override.CORSOrigins),
		RateLimit: override.RateLimit,
	}

	// Wrap in the root chain (OTel → Recover → RequestID → Logger)
	// the way the real main server does it.
	root := middleware.Chain(
		middleware.OTel(noop.NewTracerProvider()),
		middleware.Recover,
		middleware.RequestID,
		middleware.Logger,
	)
	h := server.BuildMainHandler(handlers, reg, &v1, webhookSecret)
	return httptest.NewServer(root(h))
}

// V1Override tweaks v1-chain config for a specific integration test.
type V1Override struct {
	Timeout     time.Duration
	CORSOrigins []string
	RateLimit   middleware.RateLimitConfig
}

func TestIntegration_GetDoc_ReturnsDocAndRequestID(t *testing.T) {
	srv := spinUp(t)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/v1/rfc/0001", http.NoBody)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if res.Header.Get("X-Request-ID") == "" {
		t.Error("X-Request-ID missing")
	}
	var doc domain.Document
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != "RFC-0001" {
		t.Errorf("doc.ID = %q", doc.ID)
	}
}

func TestIntegration_NotFound_ProblemJSON(t *testing.T) {
	srv := spinUp(t)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/v1/rfc/9999", http.NoBody)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != 404 {
		t.Errorf("status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `"type"`) {
		t.Errorf("body missing problem type: %s", body)
	}
}

func TestIntegration_Pagination_HeadersPresent(t *testing.T) {
	srv := spinUp(t)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/v1/rfc?limit=1", http.NoBody)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if res.Header.Get("X-Total-Count") != "2" {
		t.Errorf("X-Total-Count = %q", res.Header.Get("X-Total-Count"))
	}
	if link := res.Header.Get("Link"); !strings.Contains(link, `rel="next"`) {
		t.Errorf("Link missing next: %q", link)
	}
}

func TestIntegration_CORSPreflight(t *testing.T) {
	srv := spinUp(t)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), "OPTIONS", srv.URL+"/api/v1/rfc/0001", http.NoBody)
	req.Header.Set("Origin", "https://rfc-site.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != 204 {
		t.Errorf("status = %d, want 204", res.StatusCode)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "https://rfc-site.example" {
		t.Errorf("ACAO = %q", res.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestIntegration_RateLimit_Rejects(t *testing.T) {
	srv := spinUp(t, func(o *V1Override) {
		o.RateLimit = middleware.RateLimitConfig{
			RPS: 1, Burst: 2, Key: func(*http.Request) string { return "one" },
		}
	})
	defer srv.Close()

	get := func() *http.Response {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/v1/types", http.NoBody)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	r1 := get()
	_ = r1.Body.Close()
	r2 := get()
	_ = r2.Body.Close()
	r3 := get()
	defer func() { _ = r3.Body.Close() }()

	if r1.StatusCode != 200 || r2.StatusCode != 200 {
		t.Fatalf("burst requests: %d, %d", r1.StatusCode, r2.StatusCode)
	}
	if r3.StatusCode != http.StatusTooManyRequests {
		t.Errorf("third request: status = %d, want 429", r3.StatusCode)
	}
	if r3.Header.Get("Retry-After") == "" {
		t.Error("third request missing Retry-After header")
	}
}

func TestIntegration_Webhook_HMAC_PositiveAndNegative(t *testing.T) {
	srv := spinUp(t)
	defer srv.Close()

	body := `{"zen":"keep it simple"}`
	sign := func(secret string) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}

	// Positive
	req, _ := http.NewRequestWithContext(t.Context(), "POST", srv.URL+"/api/v1/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sign(webhookSecret))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", "id-1")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 202 {
		t.Errorf("signed request status = %d, want 202", res.StatusCode)
	}

	// Negative (wrong secret)
	req2, _ := http.NewRequestWithContext(t.Context(), "POST", srv.URL+"/api/v1/webhooks/github", strings.NewReader(body))
	req2.Header.Set("X-Hub-Signature-256", sign("wrong"))
	res2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = res2.Body.Close()
	if res2.StatusCode != 401 {
		t.Errorf("wrong-secret status = %d, want 401", res2.StatusCode)
	}
}
