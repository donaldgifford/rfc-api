---
id: DESIGN-0001
title: "rfc-api HTTP server: Go + net/http structure"
status: Draft
author: Donald Gifford
created: 2026-04-19
---
<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0001: rfc-api HTTP server: Go + net/http structure

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-04-19

<!--toc:start-->
- [Overview](#overview)
- [Goals and Non-Goals](#goals-and-non-goals)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Background](#background)
- [Detailed Design](#detailed-design)
  - [Module layout](#module-layout)
  - [Server construction](#server-construction)
  - [Middleware chain](#middleware-chain)
    - [Admin chain (admin port)](#admin-chain-admin-port)
    - [Root chain (main port)](#root-chain-main-port)
    - [/api/v1 chain (main port, versioned sub-mux)](#apiv1-chain-main-port-versioned-sub-mux)
  - [Route registration](#route-registration)
  - [Handler pattern](#handler-pattern)
  - [Error handling](#error-handling)
  - [Configuration](#configuration)
  - [Server lifecycle](#server-lifecycle)
  - [Observability hooks](#observability-hooks)
  - [OpenAPI / contract management](#openapi--contract-management)
  - [Extensibility: multiple document types](#extensibility-multiple-document-types)
- [API / Interface Changes](#api--interface-changes)
- [Data Model](#data-model)
- [Testing Strategy](#testing-strategy)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Resolved Decisions](#resolved-decisions)
- [References and canonical sources](#references-and-canonical-sources)
  - [Related project docs](#related-project-docs)
  - [Authoritative writing on Go HTTP APIs](#authoritative-writing-on-go-http-apis)
  - [Official Go packages](#official-go-packages)
  - [De facto standard libraries](#de-facto-standard-libraries)
  - [Patterns with no library (hand-written in this codebase)](#patterns-with-no-library-hand-written-in-this-codebase)
  - [External standards](#external-standards)
<!--toc:end-->

## Overview

This design doc concretizes
[ADR-0001: Use Go and the standard library net/http for rfc-api][adr-0001]
into a buildable HTTP server structure: Go module layout, `ServeMux`
setup, middleware chain, handler conventions, error handling,
configuration, graceful shutdown, and testability. It is the first
implementation design for [RFC-0001: rfc-api][rfc-0001] and covers
only the HTTP server process — storage, sync worker, and search are
the subject of separate design docs.

## Goals and Non-Goals

### Goals

- Settle package layout, handler signatures, and middleware ordering
  so Phase-1 implementation (per RFC-0001) can start without further
  discussion.
- Keep business logic out of HTTP handlers so the `net/http` seam
  stays thin and the domain code stays testable without a server.
- Define an error-to-response mapping that is uniform across every
  endpoint and does not leak internal details.
- Make the server lifecycle (start, readiness, shutdown) explicit
  and signal-safe, suitable for a Kubernetes Deployment.
- Make handlers unit-testable with `httptest.NewRecorder`, and the
  full server integration-testable via `httptest.NewServer`.
- Share the same Go module with the sync worker
  ([RFC-0001 #Service composition][rfc-0001-services]) and ship as
  sub-commands of one binary.

### Non-Goals

- Deciding on the Postgres driver, ORM, or migration tool — deferred
  to a datastore design doc under [ADR-0002][adr-0002].
- Deciding on the Meilisearch client library or index schema —
  deferred to a search design doc under [ADR-0003][adr-0003].
- Auth middleware internals — Phase 4 of RFC-0001; this doc only
  reserves the middleware slot for it.
- Prescribing rendering, theming, or any frontend concern — owned
  by [RFC-0002: rfc-site][rfc-0002].
- Locking in a specific third-party library for every cross-cutting
  concern (validator, rate-limiter, request-id generator, etc.)
  where multiple credible options exist; candidates are listed but
  the final pick is left to first-pass implementation.

## Background

RFC-0001 defines `rfc-api` as an HTTP API + sync-worker pair sharing
a Postgres database. ADR-0001 commits the HTTP tier to Go 1.26.1 and
the standard library's `net/http`. The repo is currently a skeleton
(`cmd/rfc-api/main.go` is an empty stub; no `go.mod`). The toolchain
(goreleaser, golangci-lint Uber style, mise) is already wired. See
`CLAUDE.md` for the skeleton state.

`net/http` + `ServeMux` at a glance, for readers whose last Go HTTP
work predates Go 1.22:

- A router (`http.ServeMux`) + handler stack, no framework.
- Go 1.22+ `ServeMux` supports method-aware patterns and path
  parameters: `mux.HandleFunc("GET /docs/{id}", ...)` and the
  handler retrieves the parameter with `r.PathValue("id")`.
- Handlers have the signature
  `func(w http.ResponseWriter, r *http.Request)`.
- Middleware is the standard decorator —
  `func(http.Handler) http.Handler` — composed at registration
  time.
- Route "groups" are a convention: build a sub-`ServeMux`, mount it
  at a prefix with `StripPrefix`, and wrap it in a middleware
  chain. A thin helper makes this ergonomic (see
  [#Route registration](#route-registration)).

## Detailed Design

### Module layout

```
rfc-api/
├── cmd/
│   └── rfc-api/
│       └── main.go              // CLI entrypoint; dispatches to server or worker
├── internal/
│   ├── server/                  // HTTP server (this doc's scope)
│   │   ├── server.go            // main-port Server: NewServer, Start, Shutdown
│   │   ├── admin.go             // admin-port Server: ops + pprof endpoints
│   │   ├── router.go            // route registration; versioned sub-muxes
│   │   ├── middleware/          // project-owned middleware
│   │   │   ├── chain.go         // small helper: compose []Middleware → Handler
│   │   │   ├── requestid.go
│   │   │   ├── logger.go
│   │   │   ├── recover.go
│   │   │   ├── timeout.go
│   │   │   ├── cors.go
│   │   │   ├── ratelimit.go
│   │   │   ├── githubhmac.go    // per-route: verify GitHub webhook signature
│   │   │   └── auth.go          // Phase 4; stubbed before
│   │   ├── routectx/            // request-context carrier for {typeID, pattern}
│   │   │   └── routectx.go
│   │   ├── readiness.go         // ReadinessProbe interface + registry
│   │   ├── handler/             // one file per resource
│   │   │   ├── docs.go
│   │   │   ├── search.go
│   │   │   ├── types.go         // registry introspection → /api/v1/types
│   │   │   ├── webhook.go
│   │   │   └── health.go        // /healthz + /readyz (admin port)
│   │   ├── httperr/             // typed error → HTTP mapping
│   │   │   └── httperr.go
│   │   └── render/              // response helpers (JSON, problem+json)
│   │       └── render.go
│   ├── domain/                  // business types; framework-agnostic
│   │   ├── document.go
│   │   ├── source.go
│   │   └── errors.go            // sentinel domain errors
│   ├── service/                 // use cases; no net/http, no SQL driver
│   │   └── docs.go
│   ├── store/                   // persistence (deferred design doc)
│   ├── search/                  // search client (deferred design doc)
│   ├── worker/                  // sync worker (separate design doc)
│   ├── obs/                     // metrics + tracing abstractions
│   │   ├── metrics.go           // Prometheus scrape today; swappable
│   │   └── tracing.go           // OTel setup; OTLP exporter
│   └── config/
│       └── config.go
├── pkg/                         // only for code we want consumed externally;
│                                 // empty for v1
├── api/
│   └── openapi.yaml             // hand-authored contract (see #OpenAPI)
├── Makefile
└── go.mod
```

Layout principles:

- **`internal/` by default**, `pkg/` empty until we have a deliberate
  external consumer.
- **`net/http` import is restricted to `internal/server/` and
  `cmd/rfc-api/`.** `internal/domain`, `internal/service`,
  `internal/store`, `internal/search`, and `internal/worker` must
  not import `net/http`. Domain and service layers speak in plain
  Go types; the HTTP seam is the only place request/response
  plumbing appears.
- **`cmd/rfc-api/main.go` is dispatch only.** Parses flags, loads
  config, wires dependencies, delegates to either `server.Run` or
  `worker.Run`. Two sub-commands (`serve`, `work`) so API and
  worker ship as one binary.
- **`goimports -local github.com/donaldgifford`** groups as already
  enforced by `make fmt`.

### Server construction

**Two HTTP servers, running side by side:**

- **Main server** — user traffic. Binds `RFC_API_LISTEN` (default `:8080`).
  Serves `/api/v1/*` and `POST /api/v1/webhooks/github`. Full middleware
  chain (otelhttp → recover → request-id → logger → timeout → CORS →
  rate-limit → auth).
- **Admin server** — operations and debug. Binds `RFC_API_ADMIN_LISTEN`
  (default `127.0.0.1:8081` in dev; in prod, a pod-internal port
  protected by `NetworkPolicy`). Serves `/healthz`, `/readyz`, `/metrics`,
  and optionally `/debug/pprof/*`. Short middleware chain (otelhttp →
  recover → request-id → logger). No auth, no CORS, no rate-limit, no
  timeout.

Why the split is v1, not future work:

- Prometheus scrape never passes through auth or rate-limit middleware —
  it hits the admin port, which has neither.
- Kubelet liveness/readiness probes don't count against per-IP rate-limit
  buckets.
- `/debug/pprof/profile` (CPU profile) takes 30+ seconds — it would be
  killed by the main-port timeout middleware. On admin, no timeout.
- pprof exposes detailed runtime information. Isolating it to a
  loopback-bound or NetworkPolicy-gated port removes a category of
  accidental-exposure bugs.

Both `Server` types own an `*http.Server` and wired deps. Construction
is pure; no sockets open until `Start`.

```go
// internal/server/server.go — main server
package server

type Deps struct {
    DocsSvc   *service.Docs
    SearchSvc *service.Search
    Registry  domain.DocumentTypeRegistry
    Logger    *slog.Logger
    Config    config.Server
}

type Server struct {
    deps Deps
    http *http.Server
}

func New(deps Deps) *Server                       { /* build mux, wrap middleware, build *http.Server */ }
func (s *Server) Start(ctx context.Context) error { /* ListenAndServe, Shutdown on ctx done */ }
```

```go
// internal/server/admin.go — admin server
type AdminServer struct {
    http *http.Server
}

func NewAdmin(cfg config.Admin, probes []ReadinessProbe,
    logger *slog.Logger) *AdminServer {
    /* build admin mux (healthz, readyz, metrics, optional pprof),
       wrap with admin middleware chain, build *http.Server */
}
func (s *AdminServer) Start(ctx context.Context) error { /* same shape as Server */ }
```

Wiring lives in `cmd/rfc-api`:

```go
// cmd/rfc-api/serve.go  (abridged)
cfg    := config.Load()
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
db     := store.Open(cfg.DB)
reg    := registry.New(cfg.DocumentTypes)
docsSvc := service.NewDocs(db, reg)

main  := server.New(server.Deps{DocsSvc: docsSvc, Registry: reg, Logger: logger, Config: cfg.Server})
admin := server.NewAdmin(cfg.Admin, []server.ReadinessProbe{store.Probe(db)}, logger)

ctx := signalContext()
errg, ctx := errgroup.WithContext(ctx)
errg.Go(func() error { return main.Start(ctx) })
errg.Go(func() error { return admin.Start(ctx) })
_ = errg.Wait()
```

Both servers are lifecycle-coupled through the shared signal-rooted
context: a signal cancels both; a fatal error from either cancels the
other via `errgroup`. DI is manual (constructors take interfaces), no
framework.

### Middleware chain

Middleware is `func(http.Handler) http.Handler`. A small
project-owned `Chain` helper composes a slice of middlewares into a
single wrap (~15 LOC; no third-party chain library like `alice`):

```go
// internal/server/middleware/chain.go
package middleware

type Middleware func(http.Handler) http.Handler

func Chain(mws ...Middleware) Middleware {
    return func(h http.Handler) http.Handler {
        for i := len(mws) - 1; i >= 0; i-- {
            h = mws[i](h)
        }
        return h
    }
}
```

Order matters. Three distinct chains exist:

#### Admin chain (admin port)

Wraps every request on the admin port (`/healthz`, `/readyz`,
`/metrics`, `/debug/pprof/*`). Short and unconditional:

1. **OTel tracing** — `otelhttp.NewHandler` wrap so admin traffic
   shows up in traces too (useful for debugging readiness-probe
   flakes).
2. **Recover** — catches panics.
3. **RequestID** — same behaviour as main chain.
4. **Logger (structured)** — `slog` access log.

No timeout (pprof CPU profiles are intentionally long-running), no
auth (probe-safe by being port-isolated), no CORS (no browsers),
no rate-limit (Prometheus scrapes aggressively).

#### Root chain (main port)

Wraps every request on the main port. Same four middlewares as
admin (otelhttp → recover → request-id → logger), with identical
behaviour. The separation from admin is that the main port's mux
also mounts the `/api/v1` chain on top.

#### `/api/v1` chain (main port, versioned sub-mux)

Adds the following on top of the main-port root chain, mounted
only on the versioned sub-mux:

5. **Timeout** — `context.WithTimeout(r.Context(), d)` on the
   request; default 30s, tunable per-route. Prevents long-running
   handlers from holding connections.
6. **CORS** — default-deny; allow list from config. `rfc-site`
   runs in the same cluster and typically behind the same ingress,
   so CORS is minimal for v1.
7. **Rate limit** — token-bucket via `golang.org/x/time/rate`. Key
   extraction is pluggable via a `KeyFunc(*http.Request) string`
   so Phase 4 can swap "remote IP" for "authenticated principal,
   fall back to IP." Per-key state is TTL-evicted (default TTL 1h,
   sweep every 5min) by a goroutine tied to the server's context —
   no leaked state on shutdown. The webhook path bypasses the rate
   limit; admin-port endpoints bypass by being on a different
   server entirely.
8. **Auth (Phase 4)** — OIDC JWT validation with local JWKS cache.
   Slot is reserved now; no-op in earlier phases. Mounted on
   `/api/v1` only.

The webhook endpoint (`POST /api/v1/webhooks/github`) gets a
per-route wrap with `middleware.VerifyGitHubHMAC(cfg.WebhookSecret)`
that reads the raw body, verifies the signature, and short-circuits
with 401 before any other work. Registered outside the `/api/v1`
chain (no rate limit, no auth).

### Route registration

Routes live in `router.go`. Registration is **registry-driven**:
at startup, iterate the `DocumentTypeRegistry` (see
[DESIGN-0002][design-0002]) and mount a per-type sub-mux for each
registered type under `/api/v1/{type}/`. The same set of handler
methods is reused for every type — `type` arrives as a route
parameter, never as a Go package name.

Per-type handlers need two pieces of route metadata at request time:
the type id (to reconstruct the canonical display id) and the
matched pattern template (for low-cardinality metric labels and
OTel span names). Both are captured **at registration time** by a
single wrapper closure (`withRoute`) and read from the request
context in the handler — we do **not** parse them out of `r.Pattern`
at request time. One mechanism, one place to change:

```go
// internal/server/router.go (abridged) — main port
func buildMainHandler(h handlers, reg domain.DocumentTypeRegistry,
    mw chains, cfg config.Server) http.Handler {

    main := http.NewServeMux()
    v1   := http.NewServeMux()

    // Cross-type aggregation surface.
    v1.HandleFunc("GET /types",
        withRoute("", "/api/v1/types", h.Types.List))
    v1.HandleFunc("GET /docs",
        withRoute("", "/api/v1/docs", h.Docs.ListAll))
    v1.HandleFunc("GET /search",
        withRoute("", "/api/v1/search", h.Search.Query))

    // Per-type surface, mounted once per registered DocumentType.
    for _, t := range reg.List() {
        // t.ID is the lowercase route segment: "rfc", "adr", "framework".
        prefix := "/" + t.ID
        patternBase := "/api/v1/" + t.ID

        v1.HandleFunc("GET "+prefix,
            withRoute(t.ID, patternBase, h.Docs.ListByType))
        v1.HandleFunc("GET "+prefix+"/{id}",
            withRoute(t.ID, patternBase+"/{id}", h.Docs.Get))
        v1.HandleFunc("GET "+prefix+"/{id}/links",
            withRoute(t.ID, patternBase+"/{id}/links", h.Docs.Links))
        v1.HandleFunc("GET "+prefix+"/{id}/discussion",
            withRoute(t.ID, patternBase+"/{id}/discussion", h.Docs.Discussion))
        v1.HandleFunc("GET "+prefix+"/{id}/revisions",
            withRoute(t.ID, patternBase+"/{id}/revisions", h.Docs.Revisions))
        v1.HandleFunc("GET "+prefix+"/{id}/authors",
            withRoute(t.ID, patternBase+"/{id}/authors", h.Docs.Authors))
    }

    main.Handle("/api/v1/",
        http.StripPrefix("/api/v1", mw.V1(v1)))

    main.Handle("POST /api/v1/webhooks/github",
        mw.Webhook(http.HandlerFunc(h.Webhook.GitHub)))

    return mw.Root(main)
}

// withRoute captures typeID and pattern at registration and stashes
// them on the request context. Handlers, logger, metrics middleware,
// and span namer all read from the context — never from r.Pattern.
func withRoute(typeID, pattern string, h http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        ctx := routectx.With(r.Context(), typeID, pattern)
        h(w, r.WithContext(ctx))
    }
}
```

Admin-port routing is simpler — one mux, fixed handlers, optional
pprof under a runtime flag:

```go
// internal/server/admin.go (abridged) — admin port
func buildAdminHandler(h adminHandlers, cfg config.Admin,
    mw adminChain) http.Handler {

    mux := http.NewServeMux()
    mux.HandleFunc("GET /healthz", h.Health.Live)
    mux.HandleFunc("GET /readyz",  h.Health.Ready)
    mux.Handle("GET /metrics",     promhttp.Handler())

    if cfg.PprofEnabled {
        // stdlib net/http/pprof. Only registered when the flag is on;
        // when off, the paths 404 as if they don't exist.
        mux.HandleFunc("/debug/pprof/",        pprof.Index)
        mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
        mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
        mux.HandleFunc("/debug/pprof/symbol",  pprof.Symbol)
        mux.HandleFunc("/debug/pprof/trace",   pprof.Trace)
    }

    return mw.Admin(mux)
}
```

Notes:

- `{type}` is **not** a route pattern variable. We literally string-
  concatenate the registered type id into each route path during
  registration. This avoids a wildcard catch-all that would
  require the handler to validate type at request time, and it
  gives the router a 404 for free when a client hits an unknown
  type.
- Cross-type `/docs`, `/search`, and `/types` endpoints do not
  conflict with per-type `/{type}` endpoints because Go 1.22
  `ServeMux` requires exact path segments; `/docs` and (for
  example) `/rfc` are separate entries.
- `id` is validated by the handler, not by a route regex — keeps
  routes simple and error messages uniform. The router does not
  know or care that `{id}` is numeric.
- Type-specific sub-resources (`/framework/{id}/controls`) are
  added to the loop **conditionally**, based on the type
  declaring them. For v1 all registered types get the same set;
  the conditional is a future extension.
- The webhook route is registered on the main mux (so
  `StripPrefix` does not apply and `VerifyGitHubHMAC` runs before
  anything v1).
- Admin-port routes are never rate-limited, never require auth,
  and never time out — they live on a separate `http.Server`
  bound to a non-public address. Kubelet probes Prometheus
  scrape, and pprof target that address directly.

Adding a new document type is therefore: add a `document_types`
entry in config, register its parser if it needs a new one. No
handler, router, or service code change.

Versioning: additive-only within `/api/v1`. Breaking changes get a
new sub-mux (`/api/v2`) with a documented deprecation window.

### Handler pattern

Handlers read **route metadata from the request context**, never from
`r.Pattern`. The registration closure (`withRoute`, see
[#Route registration](#route-registration)) stashes both the type id
and the matched pattern on the context at registration time. Handlers,
the structured logger, the metrics middleware, and the OTel span
namer all read from that same context key. One mechanism, one place
to change if we ever swap context propagation for something else.

```go
// internal/server/handler/docs.go
func (h *Docs) Get(w http.ResponseWriter, r *http.Request) {
    route := routectx.From(r.Context()) // {TypeID, Pattern}
    urlID := r.PathValue("id")          // "0001"

    displayID := docid.Canonical(route.TypeID, urlID) // "RFC-0001"

    doc, err := h.svc.Get(r.Context(), displayID)
    if err != nil {
        httperr.Write(w, r, err)
        return
    }
    render.JSON(w, http.StatusOK, doc)
}
```

Notes:

- The `type` segment is injected at registration, not re-derived at
  request time. Because we string-concatenate the type into the route
  during registration (see
  [#Route registration](#route-registration)), one handler function
  serves every type, and the closure carries the type through
  context.
- `docid.Canonical("rfc", "0001")` returns `"RFC-0001"`. Pure
  function, no I/O, no registry call.
- `service.Docs.Get(ctx, "RFC-0001")` is type-agnostic; the store
  looks up by the single-string composite id and returns the
  document with its `Type` field populated from the row. See
  [DESIGN-0002 #Identifier format][design-0002-id].
- The cross-type `/api/v1/docs` handler (`h.Docs.ListAll`) does
  not receive a type parameter; it paginates across all documents
  regardless of type, optionally narrowed by `?type=`.
- Why the closure + context, not `r.Pattern`: `r.Pattern` is the
  stdlib-populated matched-pattern string and technically available
  at request time. Using it for route labels / span names while
  using the closure for type id would be two sources of route
  metadata in one codebase. Consistency wins: one mechanism
  (`routectx`) carries everything, one grep scope if we ever
  change propagation.

General constraints (unchanged from earlier drafts):

- Handlers do one thing: parse input, call a service, render
  output or error. No SQL, no outbound HTTP, no parsing logic
  inline.
- Services take and return **domain types**, not HTTP types.
  Handlers do the translation.
- Handlers always pass `r.Context()` downstream. No
  `context.Background()` in handlers ever.
- Logs inside handlers are rare; most logging is middleware-driven.
  Handlers add context via `slog.With(...)` on the request-scoped
  logger if they need to.

### Error handling

Domain code returns typed errors from `internal/domain/errors.go`:

```go
var (
    ErrNotFound     = errors.New("not found")
    ErrInvalidInput = errors.New("invalid input")
    ErrConflict     = errors.New("conflict")
    ErrUpstream     = errors.New("upstream failure")
)
```

`httperr.Write(w, r, err)` maps them to HTTP responses:

| Domain error       | Status | Problem type URI            |
|--------------------|--------|-----------------------------|
| `ErrNotFound`      | 404    | `/problems/not-found`       |
| `ErrInvalidInput`  | 400    | `/problems/invalid-input`   |
| `ErrConflict`      | 409    | `/problems/conflict`        |
| `ErrUpstream`      | 502    | `/problems/upstream`        |
| anything else      | 500    | `/problems/internal`        |

Response body follows **RFC 7807 `application/problem+json`**:

```json
{
  "type": "/problems/not-found",
  "title": "Document not found",
  "status": 404,
  "detail": "no document with id RFC-9999",
  "instance": "/api/v1/docs/RFC-9999",
  "request_id": "01HX…"
}
```

Rules:

- `detail` is safe for clients: no internal file paths, no SQL, no
  stack. The full error is logged server-side with the request id
  as the join key.
- Handlers **never** encode errors directly — they call
  `httperr.Write` so shape and logging are uniform.
- A top-level "not found" and "method not allowed" response are
  handled by registering a catch-all on the root mux so the error
  envelope is consistent for routing misses too.

### Configuration

Single `config.Server` struct, loaded in this precedence:

1. Built-in defaults (safe for local dev).
2. Config file (`/etc/rfc-api/config.yaml`, optional).
3. Environment variables, via a struct-tag binder.
4. CLI flags (highest priority).

**Env var naming rule:**

- **Service-prefixed** (`RFC_API_*`) for config that `rfc-api` itself
  defines — listen addresses, log level, rate limits, timeouts,
  feature flags, webhook secrets, anything whose shape we chose.
- **Upstream-standard names** for variables tied to an external
  dependency with a well-known convention —
  `DATABASE_URL` (12-factor / most Postgres libraries),
  `MEILI_MASTER_KEY` (Meilisearch's own convention),
  `OTEL_EXPORTER_OTLP_ENDPOINT` (OTel spec), etc. Renaming these with
  a service prefix would duplicate values in every dev `.env` and
  in every cluster operator secret, for no gain.

v1 configuration surface:

| Key                            | Default                   | Purpose                                                  |
|--------------------------------|---------------------------|----------------------------------------------------------|
| `RFC_API_LISTEN`               | `:8080`                   | Main HTTP listen address (user traffic)                  |
| `RFC_API_ADMIN_LISTEN`         | `127.0.0.1:8081`          | Admin HTTP listen address (healthz/readyz/metrics/pprof) |
| `RFC_API_PPROF_ENABLED`        | `false`                   | When true, registers `/debug/pprof/*` on the admin port  |
| `RFC_API_LOG_LEVEL`            | `info`                    | `debug` / `info` / `warn` / `error`                      |
| `RFC_API_LOG_FORMAT`           | `json`                    | `json` / `text`                                          |
| `RFC_API_READ_TIMEOUT`         | `15s`                     | Main-server HTTP read timeout                            |
| `RFC_API_WRITE_TIMEOUT`        | `30s`                     | Main-server HTTP write timeout                           |
| `RFC_API_SHUTDOWN_TIMEOUT`     | `20s`                     | Graceful shutdown budget (both servers)                  |
| `RFC_API_CORS_ORIGINS`         | `""`                      | Comma list of allowed origins (main server only)         |
| `RFC_API_RATE_LIMIT_RPS`       | `50`                      | Token-bucket RPS per rate-limit key                      |
| `RFC_API_RATE_LIMIT_BURST`     | `100`                     | Token-bucket burst size                                  |
| `RFC_API_RATE_LIMIT_TTL`       | `1h`                      | Eviction TTL for per-key limiter state                   |
| `RFC_API_WEBHOOK_SECRET`       | *(required)*              | GitHub webhook HMAC secret                               |
| `RFC_API_TRACE_SAMPLE_RATIO`   | `0.1`                     | OTel head-based sampling ratio (0–1)                     |
| `DATABASE_URL`                 | *(required)*              | Postgres DSN — 12-factor standard name                   |
| `MEILI_MASTER_KEY`             | *(required)*              | Meilisearch master key — Meilisearch's own env var name  |
| `OTEL_EXPORTER_OTLP_ENDPOINT`  | *(unset → no-export)*     | OTel collector endpoint — standard OTel env var          |

No `os.Getenv` calls outside `internal/config/`.

### Server lifecycle

1. `main.go` builds a root `context.Context` tied to `SIGINT` and
   `SIGTERM`.
2. `server.New(...)` and `server.NewAdmin(...)` construct the muxes,
   wrap them with their respective chains, and build the two
   `*http.Server` instances. No sockets opened.
3. Both servers run under a shared `errgroup.Group`:
   `errg.Go(main.Start)` + `errg.Go(admin.Start)`. The errgroup's
   context is derived from the signal-rooted context, so a signal
   cancels both servers and a fatal failure from either cancels
   the other.
4. On cancel: each server's `Shutdown(shutdownCtx)` stops accepting
   new connections and drains in-flight requests within
   `RFC_API_SHUTDOWN_TIMEOUT`.
5. Exit code: 0 on graceful shutdown, 1 on listen failure, 2 on
   forced shutdown (timeout).

Readiness vs. liveness (both live on the admin port):

- `/healthz` — returns 200 if the process is up. Used by Kubernetes
  liveness probe. Cheap, no downstream calls.
- `/readyz` — returns 200 only when every registered
  `ReadinessProbe` reports healthy. Probes are plugged in via an
  interface (`Name()` + `Check(ctx) error`), so each dep registers
  its own probe at startup and the failure body names the specific
  probe that failed:
  ```json
  {"status":"not_ready","failures":[{"probe":"postgres","error":"…"}]}
  ```

Why the probe registry is an interface rather than a flat function
slice: at 3am, "`/readyz` is failing" is much more actionable when
the response body tells you *which* probe failed. Named probes make
that body easy to produce.

### Observability hooks

Both Prometheus-style scrape and OpenTelemetry are wired in v1.
They serve different roles and we don't pick one over the other:

- **Logs:** `slog` with JSON handler → stdout. Shipped to Loki by
  Grafana Alloy (tailing container stdout). Stdout is deliberate:
  platform-mediated, swappable shipper, covers crash/start/shutdown
  lines that would be lost if logs depended on an active OTLP
  exporter.
  - **Field names follow OTel logs semantic conventions**, flat
    dotted: `http.request.method`, `http.response.status_code`,
    `url.path`, `http.route`, `trace_id`, `span_id`, `request_id`.
    Flat-dotted (rather than nested) matches the OTLP canonical
    attribute shape so there's zero schema translation at the
    collector, and it's easier to scan in `kubectl logs` / `jq`.
  - `RFC_API_LOG_LEVEL` and `RFC_API_LOG_FORMAT` control output.
- **Metrics (Prometheus):** `promhttp.Handler()` mounted at
  `/metrics` on the **admin port** for Prometheus scrape. A small
  project-owned middleware records request counts and durations
  using `prometheus/client_golang`. Labels: `method`, `route`,
  `status`. **Route label is sourced from the closure-captured
  pattern on request context** (see
  [#Handler pattern](#handler-pattern)), not from the raw `path`
  or `r.Pattern` — same mechanism that carries the type id, so
  cardinality is bounded by the registered route set, never
  blown up by `{id}` values.
- **Tracing (OTel):** `otelhttp.NewHandler` is the outermost
  middleware on both servers. OTLP exporter configured from
  `OTEL_EXPORTER_OTLP_ENDPOINT` and the other standard OTel env
  vars. Server spans per request; DB spans and outgoing-HTTP spans
  inherit from them. Sampling is head-based; default 10%
  (`RFC_API_TRACE_SAMPLE_RATIO=0.1`).
  - **Span name format: `METHOD route-template`** —
    `GET /api/v1/rfc/{id}` — sourced from the same closure-captured
    pattern used for metric labels. Matches the Prometheus `route`
    label exactly, so one string filters across metrics, traces,
    and logs. The span name is set from an inner wrapper (inside
    the route closure) after the mux has dispatched, because
    `otelhttp` creates the span before routing has matched.
- **Trace ⇄ log correlation:** every log line carries the trace ID
  and span ID (as `trace_id` / `span_id` per OTel semconv). The
  RequestID middleware derives `X-Request-ID` from the trace ID
  when one is present, so a single identifier connects Prometheus
  labels, logs, and trace backends.

A future move to OTLP-push metrics (instead of Prometheus scrape)
is possible without changing handler code — metrics emission goes
through an abstraction in `internal/obs/` so the transport is
swappable.

**Future: Sentry (not v1).** We may add Sentry for error
aggregation and release-health tracking at a later date. Not a v1
commitment — logs + OTel cover the baseline, and adding Sentry
before we have operational signal about what gaps exist would be
premature. When added, it slots in as a third sink alongside logs
and traces (hooked into the Recover middleware and the domain
error path) and does not change handler code or the error-response
contract.

### OpenAPI / contract management

v1 approach: **hand-author `api/openapi.yaml`** and validate it
against handler behaviour in tests. Rationale:

- Oxide auto-generates OpenAPI from Dropshot
  ([INV-0001 #rfd-api][inv-0001-rfd-api]); Go `net/http` has no
  equivalent emitter convention.
- Generating from Go structs (swag, go-swagger) adds a codegen
  step that is awkward to keep clean under Uber-style lint, and
  tends to produce spec diffs on every refactor.
- The contract is small enough in v1 (~7 endpoints) that
  hand-authoring is cheaper than maintaining codegen
  infrastructure.

`rfc-site` and the MCP server can generate clients from
`api/openapi.yaml`; they do not need anything from the Go code.

Revisit this decision if the contract grows past ~25 endpoints or
if keeping spec/code in sync becomes a recurring PR comment.

### Extensibility: multiple document types

RFC-0001 anticipates multiple content types (RFC, ADR, frameworks,
style guides, and so on). The full treatment of how types plug in
lives in [DESIGN-0002: DocumentType extensibility][design-0002].
This subsection records the implications for the HTTP server
specifically, so the rules are visible in the same place as the
code they constrain.

**The rule, one line:** type is a parameter, not a package name.

For the HTTP server, that means:

- The handler package is `internal/server/handler/docs.go`, **not**
  `rfc.go`. Handler methods are `Docs.Get`, `Docs.Links`,
  `Docs.ListByType`, `Docs.ListAll`. `type` arrives on the route
  path, not as a Go identifier anywhere in the code.
- The route shape is `/api/v1/{type}/{id}` per type (mounted from
  the registry at startup) plus `/api/v1/docs` and
  `/api/v1/search` for cross-type aggregation. See
  [#Route registration](#route-registration) for the concrete
  loop and [DESIGN-0002 #URL structure][design-0002-url] for the
  rationale.
- Adding a new type means adding a `document_types` entry in
  config (and a parser if it needs a new one). The router loop
  picks it up at startup and mounts the full per-type endpoint
  set for it. No router edit, no handler edit, no service edit.
- `service.Docs`, `store`, `search`, and every piece of
  `internal/` other than type-specific parsers takes the canonical
  display id (or a `DocumentType` value where relevant) as an
  argument. No package under `internal/` is named after a specific
  type. No function is named `GetRFC` or `ListFrameworks`. If you
  find yourself writing one of those, stop — the abstraction has
  leaked.
- On the read hot path, the `DocumentTypeRegistry` is **not
  consulted**. The type comes out of the stored document row; the
  handler only uses the route's `{type}` segment to reconstruct
  the canonical display id (`RFC-0001` = `"RFC" + "-" + "0001"`).
  The registry's jobs are config validation at startup, driving
  the route-mount loop, populating `/api/v1/types`, and parser
  dispatch in the worker. See
  [DESIGN-0002 #Identifier format][design-0002-id].

The OpenAPI spec (`api/openapi.yaml`) follows the same shape: per-
type paths are expanded from a template or generated from the
registry at spec-author time. The cross-type `/docs` and `/search`
endpoints live in their own section.

See DESIGN-0002 for the `DocumentType` value object, the registry,
the parser plugin seam, schema extensions, per-type lifecycles,
and the list of open questions that are expected to evolve with
implementation.

## API / Interface Changes

No public API surface exists yet; this design defines it from zero.
The route set below matches [RFC-0001 #API surface][rfc-0001-api]
and is consistent with
[DESIGN-0002 #URL structure][design-0002-url]. Routes are split
across two servers per [#Server construction](#server-construction):

**Admin server (`RFC_API_ADMIN_LISTEN`, default `127.0.0.1:8081`):**

| Method | Path                  | Auth         | Middleware chain | Notes                                  |
|--------|-----------------------|--------------|------------------|----------------------------------------|
| GET    | `/healthz`            | none         | admin            | Kubelet liveness probe.                |
| GET    | `/readyz`             | none         | admin            | Kubelet readiness probe.               |
| GET    | `/metrics`            | none         | admin            | Prometheus scrape.                     |
| GET    | `/debug/pprof/*`      | none         | admin            | Only registered when `RFC_API_PPROF_ENABLED=true`. Off by default. |

Admin endpoints are port-isolated rather than auth-protected: network
policy restricts who can reach the admin port, and the port itself
is never behind an ingress.

**Main server (`RFC_API_LISTEN`, default `:8080`):**

| Method | Path                                         | Auth (Phase 4+) | Middleware chain |
|--------|----------------------------------------------|-----------------|------------------|
| GET    | `/api/v1/types`                              | `docs:read`     | v1               |
| GET    | `/api/v1/docs`                               | `docs:read`     | v1               |
| GET    | `/api/v1/search`                             | `search`        | v1               |
| GET    | `/api/v1/{type}`                             | `docs:read`     | v1               |
| GET    | `/api/v1/{type}/{id}`                        | `docs:read`     | v1               |
| GET    | `/api/v1/{type}/{id}/links`                  | `docs:read`     | v1               |
| GET    | `/api/v1/{type}/{id}/discussion`             | `docs:read`     | v1               |
| GET    | `/api/v1/{type}/{id}/revisions`              | `docs:read`     | v1 (Phase 2+)    |
| GET    | `/api/v1/{type}/{id}/authors`                | `docs:read`     | v1               |
| POST   | `/api/v1/webhooks/github`                    | HMAC only       | root + webhook   |

**`/api/v1/types` response shape:** array of registered
`DocumentType` entries, rendered from the in-process registry at
request time (no DB query, no cache):

```json
[
  {
    "id": "rfc",
    "display_prefix": "RFC",
    "title": "Request for Comments",
    "statuses": ["Draft", "Proposed", "Accepted", "Rejected", "Superseded"]
  }
]
```

Fields: `id` is the lowercase route segment, `display_prefix` is the
uppercase form in canonical ids like `RFC-0001`, `title` drives
frontend navigation, `statuses` is the per-type lifecycle set (lets
frontends build status filter dropdowns without hardcoding).

Notation:

- `{type}` is the literal registered type id (`rfc`, `adr`,
  `framework`, …), string-concatenated into the registered route
  at startup. Unknown types 404 at the router without reaching a
  handler.
- `{id}` is the numeric URL form (`0001`); canonical display
  form (`RFC-0001`) is reconstructed in the handler. See
  [#Handler pattern](#handler-pattern) and
  [DESIGN-0002 #Identifier format][design-0002-id].

**Query parameters on list endpoints** (`/api/v1/docs` and
`/api/v1/{type}`):

| Parameter | Type    | Default | Bounds             | Purpose                         |
|-----------|---------|---------|--------------------|---------------------------------|
| `limit`   | integer | `50`    | `1 ≤ limit ≤ 200`  | Page size                       |
| `cursor`  | string  | empty   | opaque, ≤ 256 chars | Pagination cursor (server-issued) |
| `type`    | string  | unset   | known type id      | Narrows `/docs` to one type; ignored on `/{type}` |
| `status`  | string  | unset   | known status       | Narrows by lifecycle state      |
| `label`   | string  | unset   | repeatable         | Narrows by frontmatter label    |

**List response shape:**

- Bare JSON array of documents (see
  [#Resolved Decisions](#resolved-decisions) #3).
- Pagination metadata in `Link` (RFC 8288; `rel="next"`,
  `rel="prev"`) and `X-Total-Count` headers.
- Each document includes its `type` field so cross-type result
  sets are unambiguous.
- **Default sort**: `(created DESC, id ASC)`. `created` is immutable,
  so pagination is stable under concurrent edits — a doc someone
  edits while a client is paging cannot jump back to page 1 and
  reappear on a later page. `id ASC` is the tiebreaker for identical
  `created` timestamps (batch imports, migrations).
- **Cursor shape**: opaque base64 of a JSON tuple
  `{"c":"2026-04-19T12:34:56.789Z","i":"RFC-0001"}`. Clients echo
  what we sent; they never construct cursors. Opaque means we can
  add fields or change the sort key in the future without breaking
  old cursors (a version byte can be added if that ever becomes
  necessary).
- **Storage prerequisite**: composite index on `(created DESC, id)`
  in Postgres. Recorded in the storage design doc.

**CLI changes:** `cmd/rfc-api` gains two sub-commands:

- `rfc-api serve [flags]` — runs the HTTP server.
- `rfc-api work [flags]` — runs the sync worker (scope of a
  separate design doc).

`rfc-api` with no sub-command prints help.

**Config changes:** new `RFC_API_*` env vars, documented above.
No compatibility concerns — there is no prior config to migrate.

## Data Model

Out of scope — the HTTP server does not own schema. All persistence
is the subject of a follow-on design doc under [ADR-0002][adr-0002].
Handlers depend on `*service.Docs` etc., which in turn take a
storage interface injected at wiring time.

## Testing Strategy

Three tiers, all using standard library primitives:

1. **Unit tests on services (`internal/service/`).** No `net/http`,
   no DB. Mock the storage interface. Cover the happy path plus
   each domain error branch. Table-driven.
2. **Handler tests (`internal/server/handler/`).** Drive handlers
   directly with `httptest.NewRequest` +
   `httptest.NewRecorder`; no server needed. Register the handler
   on a temporary `ServeMux` if you need `r.PathValue(...)` to
   resolve. Assert status, headers, JSON body shape. Cheap; run on
   every push.
3. **Integration tests (`internal/server/`).** Build a full
   `Server` against `httptest.NewServer`, a real Postgres
   (testcontainers or CI-provisioned), and a fake GitHub. Cover:
   request-id propagation, error shape, auth-middleware stub,
   rate-limit headers, 404 path, method-not-allowed path, webhook
   HMAC path (positive + negative).

What we do **not** test in v1:

- `net/http`'s own routing (it is the standard library's job).
- Real GitHub webhook delivery from GitHub (we test our verifier;
  GitHub's signing is their concern).
- Rate limit under load (a separate perf exercise if it becomes a
  concern).

**Contract tests:** a single test validates every registered route
against `api/openapi.yaml` using a spec-first validator. If the
route set or response shape drifts, that test fails.

Test layout: alongside source under the standard Go convention
(`docs_test.go` next to `docs.go`), with a top-level `test/` dir
for integration fixtures.

## Migration / Rollout Plan

This is the first implementation of the service; there is no
production state to migrate. Rollout is the normal Phase-1
progression from [RFC-0001][rfc-0001]:

1. Land `go.mod`, package skeleton, and a `net/http` "hello world"
   on `/healthz` behind the root middleware chain. Wire CI, lint,
   goreleaser (see repo `Makefile`). No domain logic yet.
2. Add `domain` types, the `DocumentTypeRegistry` (loaded from
   config with just `rfc` registered at first), and `service.Docs`
   with in-memory storage. Registry drives route-mounting;
   `GET /api/v1/docs` and `GET /api/v1/rfc/{id}` return seeded
   data. Full handler test coverage including the "fake type"
   registration test from
   [DESIGN-0002 #Testing Strategy][design-0002-testing].
3. Wire the real Postgres store behind `service.Docs` (separate
   design doc, separate PR). The HTTP tier should not change as a
   result — proof that the layering holds.
4. Deploy to the internal cluster with webhook and worker stubbed.
   Exercise the lifecycle (readiness gating, shutdown, log shape)
   before adding real traffic.

No backwards-compatibility concerns in this design (greenfield).
Future design docs assume this server structure as given.

## Resolved Decisions

Open questions from earlier drafts, resolved and recorded so the
rationale is not lost:

1. **Prometheus and OpenTelemetry — both, for v1.** Prometheus
   scrape for metrics, OTel for tracing, both wired from day one
   with trace-ID / log correlation. See
   [#Observability hooks](#observability-hooks). Metrics transport
   can move to OTLP push later without handler changes because
   emission goes through `internal/obs/`.
2. **Rate-limit backing store — per-pod in-memory for v1,
   revisit later.** Phase 1 runs a single replica so per-pod
   limits are exact. Revisit when `rfc-api` scales horizontally,
   choosing between sticky routing, a shared-state limiter
   (Redis), or accepting "good-enough" per-pod limits then.
3. **Response envelope — bare arrays with pagination headers.**
   List endpoints return bare JSON arrays; pagination metadata
   rides in `Link` and `X-Total-Count` headers. Matches Oxide's
   and GitHub v3's shape. Revisit only if pagination metadata
   becomes awkward to express in headers.
4. **`pkg/` external surface — none for v1.** Stays empty.
   Revisit if we publish a Go client SDK from this repo (parallel
   to Oxide's `rfd-sdk`); until then the OpenAPI spec is the
   contract.
5. **Single binary, two sub-commands.** Confirmed. One binary
   (`rfc-api serve`, `rfc-api work`) simplifies the container
   image — one tag, one pull, one `image:` field in two
   Deployment manifests. Split into two binaries only if
   worker-specific dependencies start bloating the API image or
   if independent scaling becomes awkward.
6. **HTTP framework vs. standard library.** Standard library
   `net/http` with Go 1.22+ `ServeMux`. Replaces an earlier draft
   that selected Echo v5. Reasoning in [ADR-0001][adr-0001].
7. **Main port + admin port, v1.** User traffic on
   `RFC_API_LISTEN`; ops endpoints (`/healthz`, `/readyz`,
   `/metrics`, optional `/debug/pprof/*`) on `RFC_API_ADMIN_LISTEN`
   with a separate short middleware chain. Admin port is
   loopback-bound in dev and NetworkPolicy-gated in prod. Kills a
   class of accidental exposures (pprof on public ingress, scrape
   through rate-limit, probe through auth) by construction rather
   than by policy.
8. **pprof gated by a runtime flag, default off.**
   `RFC_API_PPROF_ENABLED=true` registers `/debug/pprof/*` on the
   admin mux; off, the paths 404 as if they don't exist. Dev
   `.env.example` turns it on. Prod leaves it off by default; flip
   only when investigating. Less signal for an attacker poking
   the admin port.
9. **Route metadata via closure + request context, not `r.Pattern`.**
   Route-mount registration wraps each handler with a closure
   (`withRoute(typeID, pattern, handler)`) that stashes both on
   `r.Context()`. Handlers, the structured logger, the metrics
   middleware, and the OTel span namer all read from the same
   context key. One mechanism for route metadata across the
   codebase — if we ever swap context propagation for something
   else, there's a single grep scope, not two.
10. **Middleware chain helper is project-owned, not `justinas/alice`.**
    `Chain` lives in `internal/server/middleware/`, ~15 LOC. No
    third-party chain library. Single-purpose libs are fine in
    general; a ~50-LOC DIY threshold applies for utility-shaped
    code.
11. **Rate-limit key extraction is pluggable from day 1.**
    `RateLimit(ctx, rps, burst, KeyFunc)` takes a
    `func(*http.Request) string` and defaults to remote-IP
    extraction. Phase 4 auth swaps in "authenticated principal,
    fall back to IP" without changing the rate-limit middleware
    itself.
12. **Rate-limit state evicted via TTL + background sweep.**
    TTL default 1h, sweep every 5min, goroutine tied to the
    server context so it exits cleanly on shutdown. LRU-bounded
    maps and lazy eviction were considered and rejected —
    time-based eviction matches the time-based refill semantics
    of a token bucket, and leaks nothing on scraper-type
    traffic that hits once and vanishes.
13. **Env var naming — service-prefix what we define; upstream
    names for external deps.** `RFC_API_*` for our config;
    `DATABASE_URL` / `MEILI_MASTER_KEY` / `OTEL_EXPORTER_OTLP_ENDPOINT`
    unchanged because those names are defined by the upstream
    system. See [#Configuration](#configuration).
14. **Cross-type listing sort — `(created DESC, id ASC)` with
    opaque cursor.** `created` is immutable so pagination is
    stable under concurrent edits. Cursor is base64 JSON,
    opaque to clients. See [#API surface](#api--interface-changes).

## References and canonical sources

There is no single canonical "Go standard HTTP API guide." The Go
project publishes reference docs and occasional blog posts but no
curated end-to-end prescription; the community's convention is
small focused pieces over prescriptive frameworks. This section
collects the references a reader implementing against this design
should actually have open.

### Related project docs

- [RFC-0001: rfc-api — Backend API for the Markdown Portal][rfc-0001]
- [RFC-0002: rfc-site — Web Frontend for the Markdown Portal][rfc-0002]
- [ADR-0001: Use Go and the standard library net/http for rfc-api][adr-0001]
- [ADR-0002: Use PostgreSQL as the rfc-api datastore][adr-0002]
- [ADR-0003: Use Meilisearch for rfc-api search][adr-0003]
- [DESIGN-0002: DocumentType extensibility for multiple content types][design-0002]
  — sibling design doc; full treatment of the type-extensibility
  rule summarized in
  [#Extensibility](#extensibility-multiple-document-types).
- [INV-0001: Oxide RFD system — architecture case study][inv-0001]

### Authoritative writing on Go HTTP APIs

These are the de facto canon for idiomatic Go HTTP services. Read
once while implementing; the patterns in this design doc align
with them.

- **Mat Ryer, "How I write HTTP services in Go."** Three iterations
  (2014, 2018, 2024). The 2024 version is current. Covers server
  struct shape, handler patterns, middleware composition, routing,
  graceful shutdown, and testing. The closest thing to an industry
  reference for stdlib-based Go HTTP services.
- **Alex Edwards, *Let's Go* and *Let's Go Further*.** Book (paid).
  End-to-end production walk-through with stdlib `net/http` plus
  small focused libraries: middleware, validation, rate limiting,
  session management, auth, structured logging, graceful shutdown.
  The most complete single source.
- **Peter Bourgon, "Go best practices" / GopherCon talks.** Older
  but the architectural taste — layered packages, interface at the
  point of use, no framework — maps directly onto what this design
  doc lays out.

### Official Go packages

These ship with Go, live under `golang.org/x`, or are official
OpenTelemetry / Prometheus contrib modules. Prefer these before
reaching for third-party alternatives.

- [Go `net/http`](https://pkg.go.dev/net/http) — stdlib HTTP
  server and client.
- [Go 1.22 enhanced `ServeMux` patterns](https://go.dev/blog/routing-enhancements)
  — blog post covering method + path-parameter routing.
- [`log/slog`](https://pkg.go.dev/log/slog) — stdlib structured
  logging (Go 1.21+). What we use for access logs and
  domain-level logging.
- [`net/http/httptest`](https://pkg.go.dev/net/http/httptest) —
  stdlib request/response recorder and in-process test server.
  All three test tiers in [#Testing Strategy](#testing-strategy)
  use it.
- [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate)
  — official token-bucket rate limiter. What the rate-limit
  middleware wraps.
- [`otelhttp`](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp)
  — official OpenTelemetry HTTP instrumentation. Wraps an
  `http.Handler` directly; see
  [#Observability hooks](#observability-hooks).
- [`promhttp`](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/promhttp)
  — official Prometheus HTTP handler. Mounted at `/metrics`.

### De facto standard libraries

Third-party but widely adopted and audited. Small surface areas;
each solves one problem.

- [`github.com/rs/cors`](https://github.com/rs/cors) — CORS
  middleware. The common choice for stdlib services.
- [`github.com/go-playground/validator`](https://github.com/go-playground/validator)
  — struct-tag request validation. Used by the binding helper
  referenced in [#Handler pattern](#handler-pattern).
- [`github.com/justinas/alice`](https://github.com/justinas/alice)
  — tiny (~50 LOC) middleware-chain helper. Alternative to the
  project-owned `Chain` helper shown in
  [#Middleware chain](#middleware-chain); pick one, do not ship
  both.

### Patterns with no library (hand-written in this codebase)

Small enough that pulling a dependency is not justified. Each is
10–30 lines and lives in `internal/server/middleware/`.

- **Recover middleware** — `defer/recover` wrap, converts panics
  into 500s through `httperr`.
- **Request ID** — uses `crypto/rand` or derives from the OTel
  trace ID when present. See
  [#Middleware chain](#middleware-chain) step 3.
- **Access logger** — wraps the `ResponseWriter` to capture status
  and bytes written; emits one `slog` record per request.
- **Timeout** — `context.WithTimeout` on `r.Context()` plus a
  deadline-aware response path.
- **GitHub webhook HMAC verify** — `crypto/hmac` +
  `crypto/subtle.ConstantTimeCompare` against
  `X-Hub-Signature-256`. See
  [#Route registration](#route-registration).

### External standards

- [RFC 7807 — Problem Details for HTTP APIs](https://datatracker.ietf.org/doc/html/rfc7807)
  — the response shape used by `httperr`. See
  [#Error handling](#error-handling).

[rfc-0001]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
[rfc-0001-api]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md#api-surface-indicative
[rfc-0001-services]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md#service-composition-api--worker
[rfc-0002]: ../rfc/0002-rfc-site-web-frontend-for-the-markdown-portal.md
[adr-0001]: ../adr/0001-use-go-and-stdlib-net-http-for-rfc-api.md
[adr-0002]: ../adr/0002-use-postgresql-as-the-rfc-api-datastore.md
[adr-0003]: ../adr/0003-use-meilisearch-for-rfc-api-search.md
[inv-0001]: ../investigation/0001-oxide-rfd-system-architecture-case-study.md
[inv-0001-rfd-api]: ../investigation/0001-oxide-rfd-system-architecture-case-study.md#rfd-api-what-it-actually-does
[design-0002]: ./0002-documenttype-extensibility-for-multiple-content-types.md
[design-0002-url]: ./0002-documenttype-extensibility-for-multiple-content-types.md#url-structure
[design-0002-id]: ./0002-documenttype-extensibility-for-multiple-content-types.md#identifier-format
[design-0002-testing]: ./0002-documenttype-extensibility-for-multiple-content-types.md#testing-strategy
