package server

import (
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
	"github.com/donaldgifford/rfc-api/internal/server/httperr"
	"github.com/donaldgifford/rfc-api/internal/server/middleware"
	"github.com/donaldgifford/rfc-api/internal/server/routectx"
)

// Handlers bundles the handler instances the router wires onto the
// main mux. Kept flat so the router code reads as a route map, not
// a plumbing diagram.
type Handlers struct {
	Docs    *handler.Docs
	Search  *handler.Search
	Types   *handler.Types
	Webhook *handler.Webhook
}

// V1Chain groups the middleware config that lives only on the
// /api/v1 sub-mux. A zero-value RateLimitConfig disables rate
// limiting (useful in tests and one-off debug binaries).
type V1Chain struct {
	Timeout   time.Duration
	CORS      middleware.CORSConfig
	RateLimit middleware.RateLimitConfig
}

// BuildMainHandler assembles the main-port handler tree per
// DESIGN-0001 #Route registration. Cross-type aggregation endpoints
// (/types, /docs, /search) live on the v1 sub-mux alongside the
// per-type surface, which is mounted once per registered
// DocumentType.
//
// The webhook route is registered outside the /api/v1 chain (no rate
// limit, no auth). HMAC verification runs as a per-route wrap so the
// 401 contract is enforced before any other work.
func BuildMainHandler(
	h Handlers,
	reg domain.DocumentTypeRegistry,
	v1 *V1Chain,
	hmacSecret string,
) http.Handler {
	if v1 == nil {
		v1 = &V1Chain{}
	}
	main := http.NewServeMux()
	v1mux := http.NewServeMux()

	// Cross-type surface.
	v1mux.HandleFunc("GET /types",
		withRoute("", "/api/v1/types", h.Types.List))
	v1mux.HandleFunc("GET /docs",
		withRoute("", "/api/v1/docs", h.Docs.ListAll))
	v1mux.HandleFunc("GET /search",
		withRoute("", "/api/v1/search", h.Search.Query))

	// Per-type surface, mounted once per DocumentType. The type id
	// is string-concatenated into the pattern so the router 404s on
	// unknown types without a handler round-trip.
	for _, t := range reg.List() {
		prefix := "/" + t.ID
		patternBase := "/api/v1/" + t.ID

		v1mux.HandleFunc("GET "+prefix,
			withRoute(t.ID, patternBase, h.Docs.ListByType))
		v1mux.HandleFunc("GET "+prefix+"/{id}",
			withRoute(t.ID, patternBase+"/{id}", h.Docs.Get))
		v1mux.HandleFunc("GET "+prefix+"/{id}/links",
			withRoute(t.ID, patternBase+"/{id}/links", h.Docs.Links))
		v1mux.HandleFunc("GET "+prefix+"/{id}/discussion",
			withRoute(t.ID, patternBase+"/{id}/discussion", h.Docs.Discussion))
		v1mux.HandleFunc("GET "+prefix+"/{id}/revisions",
			withRoute(t.ID, patternBase+"/{id}/revisions", h.Docs.Revisions))
		v1mux.HandleFunc("GET "+prefix+"/{id}/authors",
			withRoute(t.ID, patternBase+"/{id}/authors", h.Docs.Authors))
	}

	v1chain := middleware.Chain(
		middleware.Timeout(v1.Timeout),
		middleware.CORS(&v1.CORS),
		middleware.RateLimit(v1.RateLimit),
		middleware.Auth(),
	)

	main.Handle("/api/v1/", http.StripPrefix("/api/v1", v1chain(v1mux)))

	// Webhook lives outside the v1 chain: no rate limit, no auth.
	// HMAC verification is a per-route wrap that 401s before any
	// other processing.
	webhookChain := middleware.VerifyGitHubHMAC(hmacSecret)
	main.Handle("POST /api/v1/webhooks/github",
		webhookChain(http.HandlerFunc(h.Webhook.GitHub)))

	// Catch-all for anything not matched by the per-type or cross-
	// type routes.
	main.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httperr.Write(w, r, fmt.Errorf("no route for %s %s: %w",
			r.Method, r.URL.Path, domain.ErrNotFound))
	})

	return main
}

// withRoute captures typeID and pattern at registration time and
// stashes them on r.Context() before calling handler. Route labels
// (metrics, logs, spans) read from the same key so one mechanism
// carries everything end-to-end (DESIGN-0001 #Handler pattern).
//
// After the mux has dispatched, the closure also renames the active
// OTel server span to "METHOD <pattern>" (matching the Prometheus
// route label exactly) so metrics, traces, and logs filter on the
// same string.
func withRoute(typeID, pattern string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := routectx.With(r.Context(), typeID, pattern)
		if span := trace.SpanFromContext(ctx); span != nil && span.SpanContext().IsValid() {
			span.SetName(r.Method + " " + pattern)
		}
		h(w, r.WithContext(ctx))
	}
}

// V1ChainFromConfig turns the server config into the runtime V1Chain
// used by BuildMainHandler. Kept here (not in cmd/) so tests that
// stand up the router can reuse the same translation.
func V1ChainFromConfig(srv config.Server, rl config.RateLimit) V1Chain {
	return V1Chain{
		Timeout: 30 * time.Second,
		CORS:    middleware.DefaultCORS(srv.CORSOrigins),
		RateLimit: middleware.RateLimitConfig{
			RPS:   float64(rl.RPS),
			Burst: rl.Burst,
			TTL:   rl.TTL,
		},
	}
}
