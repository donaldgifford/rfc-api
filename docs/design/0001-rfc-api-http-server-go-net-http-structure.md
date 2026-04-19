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
  ([RFC-0001 §Service composition][rfc-0001-services]) and ship as
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
  [§Route registration](#route-registration)).

## Detailed Design

### Module layout

```
rfc-api/
├── cmd/
│   └── rfc-api/
│       └── main.go              // CLI entrypoint; dispatches to server or worker
├── internal/
│   ├── server/                  // HTTP server (this doc's scope)
│   │   ├── server.go            // NewServer, Start, Shutdown
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
│   │   ├── handler/             // one file per resource
│   │   │   ├── docs.go
│   │   │   ├── search.go
│   │   │   ├── sources.go
│   │   │   ├── webhook.go
│   │   │   └── health.go
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
│   └── openapi.yaml             // hand-authored contract (see §OpenAPI)
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

Single `Server` type that owns the `http.Server` and the wired
dependencies. Construction is pure; no sockets open until `Start`.

```go
// internal/server/server.go
package server

type Deps struct {
    DocsSvc   *service.Docs
    SearchSvc *service.Search
    Logger    *slog.Logger
    Config    config.Server
}

type Server struct {
    deps Deps
    http *http.Server // constructed from a fully wrapped root handler
}

func New(deps Deps) *Server      { /* build mux, wrap middleware, build *http.Server */ }
func (s *Server) Start(ctx context.Context) error { /* ListenAndServe, Shutdown on ctx done */ }
```

Wiring lives in `cmd/rfc-api`:

```go
// cmd/rfc-api/main.go  (abridged)
cfg := config.Load()
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
db := store.Open(cfg.DB)
docsSvc := service.NewDocs(db)
srv := server.New(server.Deps{DocsSvc: docsSvc, Logger: logger, Config: cfg.Server})
_ = srv.Start(signalContext())
```

DI is manual (constructors take interfaces), no framework. Idiomatic
for a service this size and keeps startup tracing trivial.

### Middleware chain

Middleware is `func(http.Handler) http.Handler`. A small `Chain`
helper composes a slice of middlewares into a single wrap:

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

Order matters. The **root chain** wraps every request (including
health endpoints) and runs in this order (outermost first):

1. **OTel tracing** — `otelhttp.NewHandler(next, "rfc-api",
   otelhttp.WithSpanNameFormatter(...))`. Creates a server span per
   request, propagates `traceparent`, and puts the span into
   `r.Context()` so downstream HTTP and DB calls inherit it
   automatically. Outermost so it also covers recover and logging.
2. **Recover** — catches panics from anything below, logs with
   stack, returns a 500 via the `httperr` path.
3. **RequestID** — assigns or propagates `X-Request-ID`, attaches
   it to the request context, and derives from the OTel trace ID
   when one is present so a single identifier links logs, metrics
   labels, and trace backends.
4. **Logger (structured)** — emits one access log per request with
   method, matched route, status, duration, size, request ID, and
   trace ID. Uses `slog`.

The **`/api/v1` chain** adds the following on top of the root chain,
mounted only on the versioned sub-mux:

5. **Timeout** — `context.WithTimeout(r.Context(), d)` on the
   request; default 30s, tunable per-route. Prevents long-running
   handlers from holding connections.
6. **CORS** — default-deny; allow list from config. `rfc-site`
   runs in the same cluster and typically behind the same ingress,
   so CORS is minimal for v1.
7. **Rate limit** — per-IP token-bucket. Limits are config-driven.
   Health endpoints and the webhook are not rate-limited.
8. **Auth (Phase 4)** — OIDC JWT validation with local JWKS cache.
   Slot is reserved now; no-op in earlier phases. Mounted on
   `/api/v1`, not on `/healthz`, `/readyz`, or `/webhooks/*`.

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

```go
// internal/server/router.go (abridged)
func buildHandler(h handlers, reg domain.DocumentTypeRegistry,
    mw chains, cfg config.Server) http.Handler {

    root := http.NewServeMux()
    root.HandleFunc("GET /healthz", h.Health.Live)
    root.HandleFunc("GET /readyz",  h.Health.Ready)
    root.Handle("GET /metrics",     promhttp.Handler())

    v1 := http.NewServeMux()

    // Cross-type aggregation surface.
    v1.HandleFunc("GET /sources",   h.Sources.List)
    v1.HandleFunc("GET /docs",      h.Docs.ListAll)    // paginated cross-type
    v1.HandleFunc("GET /search",    h.Search.Query)

    // Per-type surface, mounted once per registered DocumentType.
    for _, t := range reg.List() {
        // t.ID is the lowercase route segment: "rfc", "adr", "framework".
        prefix := "/" + t.ID
        v1.HandleFunc("GET "+prefix,
            h.Docs.ListByType)                                  // {type}
        v1.HandleFunc("GET "+prefix+"/{id}",
            h.Docs.Get)                                          // {type}/{id}
        v1.HandleFunc("GET "+prefix+"/{id}/links",
            h.Docs.Links)                                        // {type}/{id}/links
        v1.HandleFunc("GET "+prefix+"/{id}/discussion",
            h.Docs.Discussion)                                   // {type}/{id}/discussion
        v1.HandleFunc("GET "+prefix+"/{id}/revisions",
            h.Docs.Revisions)                                    // Phase 2+
        v1.HandleFunc("GET "+prefix+"/{id}/authors",
            h.Docs.Authors)                                      // {type}/{id}/authors
    }

    root.Handle("/api/v1/",
        http.StripPrefix("/api/v1", mw.V1(v1)))

    root.Handle("POST /api/v1/webhooks/github",
        mw.Webhook(http.HandlerFunc(h.Webhook.GitHub)))

    return mw.Root(root)
}
```

Notes:

- `{type}` is **not** a route pattern variable. We literally string-
  concatenate the registered type id into each route path during
  registration. This avoids a wildcard catch-all that would
  require the handler to validate type at request time, and it
  gives the router a 404 for free when a client hits an unknown
  type.
- Cross-type `/docs` and `/search` endpoints do not conflict with
  per-type `/{type}` endpoints because Go 1.22 `ServeMux` requires
  exact path segments; `/docs` and (for example) `/rfc` are
  separate entries.
- `id` is validated by the handler, not by a route regex — keeps
  routes simple and error messages uniform. The router does not
  know or care that `{id}` is numeric.
- Type-specific sub-resources (`/framework/{id}/controls`) are
  added to the loop **conditionally**, based on the type
  declaring them. For v1 all registered types get the same set;
  the conditional is a future extension.
- The webhook route is registered on the root mux (so
  `StripPrefix` does not apply and `VerifyGitHubHMAC` runs before
  anything v1).

Adding a new document type is therefore: add a `document_types`
entry in config, register its parser if it needs a new one. No
handler, router, or service code change.

Versioning: additive-only within `/api/v1`. Breaking changes get a
new sub-mux (`/api/v2`) with a documented deprecation window.

### Handler pattern

Handlers on per-type routes derive both `type` and `id` (URL form)
from the path. The handler reconstructs the canonical display id
(`RFC-0001`) and passes that to the service. **No prefix-parsing
or registry lookup on the read hot path.**

```go
// internal/server/handler/docs.go
func (h *Docs) Get(w http.ResponseWriter, r *http.Request) {
    // Route is /api/v1/{type}/{id}; both are set by the router.
    typeID := routeSegmentForCurrentType(r) // "rfc", "adr", ...
    urlID  := r.PathValue("id")             // "0001"

    displayID := docid.Canonical(typeID, urlID) // "RFC-0001"

    doc, err := h.svc.Get(r.Context(), displayID)
    if err != nil {
        httperr.Write(w, r, err) // see §Error handling
        return
    }
    render.JSON(w, http.StatusOK, doc)
}
```

Notes:

- The `type` segment is derived from the route path, not the path
  variable. Because we string-concatenate the type into the route
  during registration (see [§Route registration](#route-registration)),
  one handler function serves every type. The helper
  `routeSegmentForCurrentType(r)` reads the matched pattern from
  `r.Pattern` (Go 1.22+) and extracts the `{type}` segment, or —
  simpler — the registry loop in `router.go` wraps each registered
  handler with a tiny closure that carries the type:
  `v1.HandleFunc("GET "+prefix+"/{id}", withType(t.ID, h.Docs.Get))`.
- `docid.Canonical("rfc", "0001")` returns `"RFC-0001"`. Pure
  function, no I/O, no registry call.
- `service.Docs.Get(ctx, "RFC-0001")` is type-agnostic; the store
  looks up by the single-string composite id and returns the
  document with its `Type` field populated from the row. See
  [DESIGN-0002 §Identifier format][design-0002-id].
- The cross-type `/api/v1/docs` handler (`h.Docs.ListAll`) does
  not receive a type parameter; it paginates across all documents
  regardless of type, optionally narrowed by `?type=`.

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
3. Environment variables (`RFC_API_*`), via a struct-tag binder.
4. CLI flags (highest priority).

v1 configuration surface:

| Key                          | Default               | Purpose                               |
|------------------------------|-----------------------|---------------------------------------|
| `RFC_API_LISTEN`             | `:8080`               | HTTP listen address                   |
| `RFC_API_LOG_LEVEL`          | `info`                | `debug` / `info` / `warn` / `error`   |
| `RFC_API_LOG_FORMAT`         | `json`                | `json` / `text`                       |
| `RFC_API_READ_TIMEOUT`       | `15s`                 | HTTP read timeout                     |
| `RFC_API_WRITE_TIMEOUT`      | `30s`                 | HTTP write timeout                    |
| `RFC_API_SHUTDOWN_TIMEOUT`   | `20s`                 | Graceful shutdown budget              |
| `RFC_API_CORS_ORIGINS`       | `""`                  | Comma list of allowed origins         |
| `RFC_API_RATE_LIMIT_RPS`     | `50`                  | Per-IP requests per second            |
| `RFC_API_WEBHOOK_SECRET`     | *(required)*          | GitHub webhook HMAC secret            |
| `RFC_API_DB_URL`             | *(required)*          | Postgres DSN                          |
| `RFC_API_TRACE_SAMPLE_RATIO` | `0.1`                 | OTel head-based sampling ratio (0–1)  |
| `OTEL_EXPORTER_OTLP_ENDPOINT`| *(unset → no-export)* | OTel collector endpoint; standard OTel env var |

No `os.Getenv` calls outside `internal/config/`.

### Server lifecycle

1. `main.go` builds a root `context.Context` tied to `SIGINT` and
   `SIGTERM`.
2. `server.New(...)` constructs the mux, wraps it with the
   middleware chain, and builds the `*http.Server`. No sockets
   opened.
3. `server.Start(ctx)` binds the listener and serves until `ctx`
   is cancelled.
4. On cancel: `srv.Shutdown(shutdownCtx)` stops accepting new
   connections and drains in-flight requests within
   `ShutdownTimeout`.
5. Exit code: 0 on graceful shutdown, 1 on listen failure, 2 on
   forced shutdown (timeout).

Readiness vs. liveness:

- `/healthz` — returns 200 if the process is up. Used by Kubernetes
  liveness probe. Cheap, no downstream calls.
- `/readyz` — returns 200 only if the DB is reachable and
  migrations are current. Used by the readiness probe. Gating the
  service out of the load balancer during startup/shutdown is what
  this is for.

### Observability hooks

Both Prometheus-style scrape and OpenTelemetry are wired in v1.
They serve different roles and we don't pick one over the other:

- **Logs:** `slog` with JSON handler, `RFC_API_LOG_LEVEL` and
  `RFC_API_LOG_FORMAT` control output. Request ID and trace ID are
  always logged; user id when auth lands.
- **Metrics (Prometheus):** `promhttp.Handler()` mounted at
  `/metrics` for Prometheus scrape. A small project-owned
  middleware records request counts and durations using
  `prometheus/client_golang`. Labels: `method`, `route`, `status`.
  **Do not label by raw `path`** to avoid cardinality blow-up from
  `{id}` — use the matched route template, which we derive from the
  sub-mux registration.
- **Tracing (OTel):** `otelhttp.NewHandler` is the outermost
  middleware. OTLP exporter configured from
  `OTEL_EXPORTER_OTLP_ENDPOINT` and the other standard OTel env
  vars. Server spans per request, DB spans and outgoing-HTTP spans
  inherit from them. Sampling is head-based and configurable;
  default is 10% for v1. `otelhttp` is first-class for stdlib
  `net/http` — a concrete benefit of staying off a framework.
- **Trace ⇄ log correlation:** every log line carries the trace ID
  and span ID from the active context. The RequestID middleware
  derives its value from the trace ID when one is present so a
  single identifier connects Prometheus labels, logs, and trace
  backends.

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
  ([INV-0001 §rfd-api][inv-0001-rfd-api]); Go `net/http` has no
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
  [§Route registration](#route-registration) for the concrete
  loop and [DESIGN-0002 §URL structure][design-0002-url] for the
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
  the route-mount loop, populating `/api/v1/sources`, and parser
  dispatch in the worker. See
  [DESIGN-0002 §Identifier format][design-0002-id].

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
The route set below matches [RFC-0001 §API surface][rfc-0001-api]
and is consistent with
[DESIGN-0002 §URL structure][design-0002-url]:

| Method | Path                                         | Auth (Phase 4+) | Middleware chain |
|--------|----------------------------------------------|-----------------|------------------|
| GET    | `/healthz`                                   | none            | root             |
| GET    | `/readyz`                                    | none            | root             |
| GET    | `/metrics`                                   | none            | root             |
| GET    | `/api/v1/sources`                            | `docs:read`     | v1               |
| GET    | `/api/v1/docs`                               | `docs:read`     | v1               |
| GET    | `/api/v1/search`                             | `search`        | v1               |
| GET    | `/api/v1/{type}`                             | `docs:read`     | v1               |
| GET    | `/api/v1/{type}/{id}`                        | `docs:read`     | v1               |
| GET    | `/api/v1/{type}/{id}/links`                  | `docs:read`     | v1               |
| GET    | `/api/v1/{type}/{id}/discussion`             | `docs:read`     | v1               |
| GET    | `/api/v1/{type}/{id}/revisions`              | `docs:read`     | v1 (Phase 2+)    |
| GET    | `/api/v1/{type}/{id}/authors`                | `docs:read`     | v1               |
| POST   | `/api/v1/webhooks/github`                    | HMAC only       | root + webhook   |

Notation:

- `{type}` is the literal registered type id (`rfc`, `adr`,
  `framework`, …), string-concatenated into the registered route
  at startup. Unknown types 404 at the router without reaching a
  handler.
- `{id}` is the numeric URL form (`0001`); canonical display
  form (`RFC-0001`) is reconstructed in the handler. See
  [§Handler pattern](#handler-pattern) and
  [DESIGN-0002 §Identifier format][design-0002-id].

**Query parameters on list endpoints** (`/api/v1/docs` and
`/api/v1/{type}`):

| Parameter | Type    | Default | Bounds             | Purpose                         |
|-----------|---------|---------|--------------------|---------------------------------|
| `limit`   | integer | `50`    | `1 ≤ limit ≤ 200`  | Page size                       |
| `cursor`  | string  | empty   | opaque, ≤ 256 chars | Pagination cursor (server-issued) |
| `type`    | string  | unset   | known type id      | Narrows `/docs` to one type; ignored on `/{type}` |
| `status`  | string  | unset   | known status       | Narrows by lifecycle state      |
| `label`   | string  | unset   | repeatable         | Narrows by frontmatter label    |

**Cross-type list (`/api/v1/docs`) response:**

- Bare JSON array of documents (see
  [§Resolved Decisions](#resolved-decisions) #3).
- Pagination metadata in `Link` (RFC 8288; `rel="next"`,
  `rel="prev"`) and `X-Total-Count` headers.
- Each document includes its `type` field so cross-type result
  sets are unambiguous.

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
   [DESIGN-0002 §Testing Strategy][design-0002-testing].
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
   [§Observability hooks](#observability-hooks). Metrics transport
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
  [§Extensibility](#extensibility-multiple-document-types).
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
  All three test tiers in [§Testing Strategy](#testing-strategy)
  use it.
- [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate)
  — official token-bucket rate limiter. What the rate-limit
  middleware wraps.
- [`otelhttp`](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp)
  — official OpenTelemetry HTTP instrumentation. Wraps an
  `http.Handler` directly; see
  [§Observability hooks](#observability-hooks).
- [`promhttp`](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/promhttp)
  — official Prometheus HTTP handler. Mounted at `/metrics`.

### De facto standard libraries

Third-party but widely adopted and audited. Small surface areas;
each solves one problem.

- [`github.com/rs/cors`](https://github.com/rs/cors) — CORS
  middleware. The common choice for stdlib services.
- [`github.com/go-playground/validator`](https://github.com/go-playground/validator)
  — struct-tag request validation. Used by the binding helper
  referenced in [§Handler pattern](#handler-pattern).
- [`github.com/justinas/alice`](https://github.com/justinas/alice)
  — tiny (~50 LOC) middleware-chain helper. Alternative to the
  project-owned `Chain` helper shown in
  [§Middleware chain](#middleware-chain); pick one, do not ship
  both.

### Patterns with no library (hand-written in this codebase)

Small enough that pulling a dependency is not justified. Each is
10–30 lines and lives in `internal/server/middleware/`.

- **Recover middleware** — `defer/recover` wrap, converts panics
  into 500s through `httperr`.
- **Request ID** — uses `crypto/rand` or derives from the OTel
  trace ID when present. See
  [§Middleware chain](#middleware-chain) step 3.
- **Access logger** — wraps the `ResponseWriter` to capture status
  and bytes written; emits one `slog` record per request.
- **Timeout** — `context.WithTimeout` on `r.Context()` plus a
  deadline-aware response path.
- **GitHub webhook HMAC verify** — `crypto/hmac` +
  `crypto/subtle.ConstantTimeCompare` against
  `X-Hub-Signature-256`. See
  [§Route registration](#route-registration).

### External standards

- [RFC 7807 — Problem Details for HTTP APIs](https://datatracker.ietf.org/doc/html/rfc7807)
  — the response shape used by `httperr`. See
  [§Error handling](#error-handling).

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
