---
id: IMPL-0001
title: "rfc-api HTTP server phase 1 implementation"
status: Completed
author: Donald Gifford
created: 2026-04-19
completed: 2026-04-20
---
<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0001: rfc-api HTTP server phase 1 implementation

**Status:** Completed
**Author:** Donald Gifford
**Date:** 2026-04-19
**Completed:** 2026-04-20

<!--toc:start-->
- [Objective](#objective)
- [Scope](#scope)
  - [In Scope](#in-scope)
  - [Out of Scope](#out-of-scope)
- [Prerequisites: local development stack](#prerequisites-local-development-stack)
  - [Stack shape](#stack-shape)
  - [Tasks](#tasks)
  - [Success Criteria](#success-criteria)
- [Implementation Phases](#implementation-phases)
  - [Phase 1: Foundation — module, CLI, lifecycle, both servers](#phase-1-foundation--module-cli-lifecycle-both-servers)
    - [Tasks](#tasks-1)
    - [Success Criteria](#success-criteria-1)
  - [Phase 2: Routes, handlers, and the DocumentType seam](#phase-2-routes-handlers-and-the-documenttype-seam)
    - [Tasks](#tasks-2)
    - [Success Criteria](#success-criteria-2)
  - [Phase 3: Observability, contract, and hardening](#phase-3-observability-contract-and-hardening)
    - [Tasks](#tasks-3)
    - [Success Criteria](#success-criteria-3)
- [File Changes](#file-changes)
- [Testing Plan](#testing-plan)
- [Dependencies](#dependencies)
- [Resolved Decisions](#resolved-decisions)
- [References](#references)
<!--toc:end-->

## Objective

Turn [DESIGN-0001: rfc-api HTTP server — Go + net/http structure][design-0001]
into working code: Go module, `cmd/rfc-api` sub-commands, `internal/server`
with `ServeMux` routing and middleware chain, handler set, RFC 7807 error
envelope, metrics and tracing wiring, and a testable lifecycle.

The end state of this IMPL is a single-replica HTTP server with in-memory
storage that serves every endpoint in the DESIGN's #API surface table,
is fully unit- and integration-tested, and produces the log / metric /
trace signals DESIGN-0001 commits to — ready for the real Postgres store
(separate design doc) to be slotted in behind `service.Docs` without any
handler change.

**Implements:** [DESIGN-0001][design-0001] (Phase 1 of
[RFC-0001][rfc-0001]).

## Scope

### In Scope

- `go mod init github.com/donaldgifford/rfc-api` and toolchain bring-up
  against existing `Makefile` / `.golangci.yml` / `mise.toml`.
- `cmd/rfc-api` with `serve` and `work` sub-commands (work stubbed).
- `internal/server/` — construction, routing, middleware chain, handler
  set, `httperr`, `render`.
- `internal/domain/` — document value types, sentinel errors,
  `DocumentType` and `DocumentTypeRegistry` interfaces per
  [DESIGN-0002][design-0002].
- `internal/service/docs.go` — use-case layer, storage interface,
  in-memory implementation for Phase 2 testing.
- `internal/config/` — env + file + flag precedence, `RFC_API_*` surface
  from [DESIGN-0001 #Configuration][design-0001-config].
- `internal/obs/` — metrics and tracing abstractions; `promhttp`
  `/metrics` endpoint; `otelhttp` wrap as outermost middleware.
- `api/openapi.yaml` — hand-authored contract for every endpoint.
- Full unit + handler + integration test coverage against the DESIGN's
  three-tier strategy.
- Prerequisite CI fixes flagged in `CLAUDE.md`: add `docker-bake.hcl`
  and correct the `make run` target.

### Out of Scope

- Real Postgres store. A stub `store` package may exist to satisfy
  imports, but schema, migrations, and driver choice are deferred to
  the datastore design doc under [ADR-0002][adr-0002].
- Meilisearch integration (per [ADR-0003][adr-0003]); `/api/v1/search`
  returns an empty page in Phase 2 and is wired against a
  `search.Client` interface that ships with a no-op implementation.
- Sync worker internals. `rfc-api work` registers, parses flags, logs
  "not yet implemented," and exits 0 — a deliberate placeholder.
- Real OIDC auth. Auth middleware is a no-op pass-through in v1
  (DESIGN-0001 #Middleware chain step 8); Phase 4 of RFC-0001 replaces
  it.
- `rfc-site` frontend ([RFC-0002][rfc-0002]).
- Perf work (rate-limit under load, concurrent-read benchmarks).
- Sentry integration (flagged "future" in DESIGN-0001).

## Prerequisites: local development stack

Before Phase 1 begins, stand up a local `docker compose` stack that
hosts every external dependency `rfc-api` will eventually integrate
with. This is **dependency hosting only** — the `rfc-api` binary is
never built or run inside compose during development. The dev loop
stays `go run ./cmd/rfc-api serve` on the host, pointed at
compose-hosted services (`localhost:5432`, `localhost:7700`, …).
`docker build` is reserved for goreleaser, CI, and release.

Why this lands first:

- `/healthz` and `/readyz` (Phase 1) are pointless without something
  to probe. Even a no-op readiness probe becomes real once there's
  a Postgres to talk to.
- Phase 2 handlers go through `service.Docs` and `search.Client`
  interfaces, but the only way to prove the seams are honest is to
  point them at the real Postgres / Meilisearch — otherwise
  "swappable" is a claim we haven't tested.
- Phase 3 observability tuning (sampler ratios, Prometheus buckets,
  span name cardinality) is guesswork without a collector and
  backends running locally.

### Stack shape

One `compose.yaml` at repo root, services tagged into profiles so
a developer brings up only what they need. `docker compose up`
with no profile starts the **default** services; any profile can
be composed in with `--profile <name>` or the `COMPOSE_PROFILES`
env var.

**Default profile (always on):**

| Service       | Image                                        | Host port | Role                                                 |
|---------------|----------------------------------------------|-----------|------------------------------------------------------|
| `postgres`    | `postgres:18-alpine`                         | 5432      | Primary datastore ([ADR-0002][adr-0002]).            |
| `meilisearch` | `getmeili/meilisearch:v1`                    | 7700      | Search index ([ADR-0003][adr-0003]).                 |

**Profile `auth`:**

| Service     | Image                                | Host port | Role                                                              |
|-------------|--------------------------------------|-----------|-------------------------------------------------------------------|
| `keycloak`  | `quay.io/keycloak/keycloak:26`       | 8180      | Dev OIDC provider (RFC-0001 #Technology choices). Realm seeded from `deploy/dev/keycloak/rfc-api-realm.json`. |

**Profile `tracing`:**

| Service          | Image                                                 | Host port   | Role                                               |
|------------------|-------------------------------------------------------|-------------|----------------------------------------------------|
| `otel-collector` | `otel/opentelemetry-collector-contrib:latest`         | 4317, 4318  | OTLP receiver from `rfc-api`; fans out to Jaeger / Prometheus / Loki. |
| `jaeger`         | `jaegertracing/all-in-one:latest`                     | 16686       | Trace UI.                                          |

**Profile `metrics`:**

| Service       | Image                        | Host port | Role                                                                 |
|---------------|------------------------------|-----------|----------------------------------------------------------------------|
| `prometheus`  | `prom/prometheus:latest`     | 9090      | Scrapes `host.docker.internal:8081/metrics` from the admin port of the host-run binary. |
| `grafana`     | `grafana/grafana:latest`     | 3000      | Dashboards, datasources provisioned from `deploy/dev/grafana/`.      |

**Profile `logs`:**

| Service | Image                    | Host port | Role                                                      |
|---------|--------------------------|-----------|-----------------------------------------------------------|
| `loki`  | `grafana/loki:latest`    | 3100      | Log store; datasource added to the Grafana above.         |
| `alloy` | `grafana/alloy:latest`   | 12345     | Log shipper — tails compose container stdout into Loki.   |

Port choices avoid conflicts with `rfc-api` (8080) and with common
dev tools. All services use named volumes for state so
`compose down` does not wipe data.

### Tasks

- [x] Create `compose.yaml` at repo root with the services above,
      `profiles:` tags per the table, named volumes
      (`pg_data`, `meili_data`, `keycloak_data`, `jaeger_data`,
      `prom_data`, `grafana_data`, `loki_data`), healthchecks on
      `postgres` and `meilisearch`, and an internal bridge network
      so services resolve each other by name.
- [x] `.env.example` at repo root seeded with dev values that
      target the compose stack. Service-prefixed for config we
      own; upstream-standard names for external deps (see
      DESIGN-0001 #Configuration):
      `RFC_API_LISTEN=:8080`,
      `RFC_API_ADMIN_LISTEN=127.0.0.1:8081`,
      `RFC_API_PPROF_ENABLED=true`,
      `RFC_API_WEBHOOK_SECRET=dev-webhook-secret`,
      `DATABASE_URL=postgres://rfcapi:rfcapi@localhost:5432/rfcapi`,
      `MEILI_MASTER_KEY=dev-master-key`,
      `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317`.
      `.env` is gitignored; developer copies `.env.example` on
      first run.
- [x] `deploy/dev/` tree with per-service configs:
  - `keycloak/rfc-api-realm.json` — seeded realm + dev clients
    (`rfc-api` resource server, `rfc-site` public client).
  - `otel/otel-collector.yaml` — OTLP receivers; exporters to
    Jaeger, Prometheus remote-write, and Loki.
  - `prometheus/prometheus.yml` — scrape config targeting
    `host.docker.internal:8081` (admin port of the host-run
    binary) and the collector itself.
  - `grafana/provisioning/datasources/` — Prometheus, Loki,
    Jaeger datasources.
  - `grafana/provisioning/dashboards/` — one starter dashboard
    (request rate, p50/p95/p99 latency, error rate, in-flight).
  - `alloy/config.alloy` — tail compose container logs into
    Loki with labels that mirror the span/log correlation
    story in DESIGN-0001 #Observability hooks.
- [x] `Makefile` additions (preserve existing Uber-style target
      conventions):
  - `make compose-up` — default profile only (Postgres + Meilisearch).
  - `make compose-up-auth` — default + `auth`.
  - `make compose-up-obs` — default + `tracing` + `metrics` + `logs`.
  - `make compose-up-full` — every profile.
  - `make compose-down` — stop, keep volumes.
  - `make compose-nuke` — stop and remove volumes (gated on an
    interactive confirmation or `CONFIRM=1`).
  - `make compose-logs SERVICE=<name>` — convenience tail.
- [x] CLAUDE.md and/or a short `docs/local-dev.md`:
      one-command getting-started, port map, troubleshooting for
      `host.docker.internal` on Linux (`--add-host=host.docker.internal:host-gateway`).
- [x] `.gitignore` additions: `.env`, any compose override files
      (`compose.override.yaml`).
- [x] Verify a clean-clone smoke test:
      `mise install && go mod tidy && cp .env.example .env && make compose-up && go run ./cmd/rfc-api serve`
      reaches a healthy state without manual intervention.
      _(Compose half verified end-to-end: postgres + meilisearch
      reach healthy in ~10s; healthcheck required the 127.0.0.1 fix
      noted in compose.yaml. The `go mod tidy` / `go run` half is
      verified as Phase 1 Go code lands.)_

### Success Criteria

- `make compose-up` from a cold Docker cache brings Postgres and
  Meilisearch healthy in under 60s; subsequent starts under 10s.
- A developer on a fresh clone can `cp .env.example .env &&
  make compose-up && go run ./cmd/rfc-api serve` and have a
  running, DB-connected binary with **zero** manual configuration.
- Each profile is independently togglable; bringing up `tracing`
  does not pull in `metrics` or `logs` services.
- `make compose-down` preserves data; `make compose-nuke`
  discards it and prompts before doing so.
- The Grafana instance (profile `metrics` + `logs` + `tracing`)
  boots with Prometheus, Loki, and Jaeger datasources already
  wired via provisioning — no manual clicks.
- No compose service is configured with `restart: always` in dev
  — local failures should surface, not self-heal into silence.
- `compose.yaml` passes `docker compose config --quiet` (schema
  valid) and `yamllint` at the repo's configured rules.

---

## Implementation Phases

Each phase builds on the previous one. A phase is complete when all
tasks are checked off **and** every success criterion is observably
met (lint, tests, or explicit behavioural check). Phases are landed
as separate PRs.

---

### Phase 1: Foundation — module, CLI, lifecycle, both servers

Stand up the shell: a compilable Go module with a `rfc-api serve`
sub-command that binds **both the main server (`RFC_API_LISTEN`)
and the admin server (`RFC_API_ADMIN_LISTEN`)**, serves baseline
ops endpoints (`/healthz`, `/readyz`, `/metrics`, optional
`/debug/pprof/*`) on the admin port, and shuts both servers down
gracefully on SIGTERM. No domain logic, no routes under `/api/v1`,
no `DocumentType` yet.

Goal: a deployable container image running a healthy, observable
HTTP process (two ports, same binary) that the Phase-1 cluster work
in RFC-0001 can be pointed at without further changes to `main`,
`server`, `admin`, or the middleware chains.

#### Tasks

**Module + toolchain**

- [x] `go mod init github.com/donaldgifford/rfc-api`; set
      `go 1.26.1` to match `mise.toml`. _(Done in prior commit
      `e7c0314`; go.mod verified correct.)_
- [x] Add `docker-bake.hcl` at repo root with a `ci` target that
      matches what `.github/workflows/ci.yml`'s `docker-build` job
      expects (multi-arch build, GHA cache).
- [x] Fix `Makefile` `run` target: point `./build/bin/repo-guardian`
      → `$(BIN_DIR)/$(PROJECT_NAME)` so `make run` works.
- [x] Confirm `make fmt`, `make lint`, `make test`, `make build`,
      `make ci` all succeed on the empty skeleton. _(Required adding
      a LICENSE file for `license-check`; all five targets pass.)_

**CLI entrypoint**

- [x] `cmd/rfc-api/main.go`: top-level dispatch reading the first
      positional arg (`serve` | `work` | empty → help). Wire
      `main.version` / `main.commit` ldflags per `Makefile`.
- [x] `cmd/rfc-api/serve.go`: parses `serve`-specific flags, builds
      signal-rooted context (`SIGINT` + `SIGTERM`), calls
      `server.New(...).Start(ctx)`. _(Signal context + flag parsing
      in place; real server wiring is a placeholder until the
      "Server construction + lifecycle" task block lands.)_
- [x] `cmd/rfc-api/work.go`: stub that logs "worker started /
      stopped" at INFO and blocks on ctx (per Resolved Decision 6,
      which supersedes the earlier "exits 0" wording).
- [x] Exit codes: 0 graceful, 1 startup failure, 2 shutdown timeout
      exceeded. Implemented via `exitCodeFor`; verified by
      `make smoke`.

**Config**

- [x] `internal/config/config.go`: `Server` and `Admin` structs
      covering every key in DESIGN-0001 #Configuration —
      service-prefixed (`RFC_API_LISTEN`, `RFC_API_ADMIN_LISTEN`,
      `RFC_API_PPROF_ENABLED`, `RFC_API_LOG_LEVEL`,
      `RFC_API_RATE_LIMIT_RPS`, `RFC_API_RATE_LIMIT_BURST`,
      `RFC_API_RATE_LIMIT_TTL`, `RFC_API_TRACE_SAMPLE_RATIO`,
      `RFC_API_WEBHOOK_SECRET`, timeouts) plus upstream-standard
      names (`DATABASE_URL`, `MEILI_MASTER_KEY`,
      `OTEL_EXPORTER_OTLP_ENDPOINT`).
- [x] Loader precedence: defaults → optional `/etc/rfc-api/config.yaml`
      → env vars → CLI flags. Each source tested individually +
      combined precedence.
- [x] No `os.Getenv` outside this package. Enforced by
      `internal/config/lint_test.go:TestNoOsGetenvOutsideConfig`
      which walks `cmd/` + `internal/` and fails on any hit.
- [x] Required-fields validation (`DATABASE_URL`,
      `MEILI_MASTER_KEY`, `RFC_API_WEBHOOK_SECRET`) — fails fast at
      startup, clear error message naming which var(s) are missing.

**Server construction + lifecycle (both servers)**

- [x] `internal/server/server.go`: `Deps`, `Server`, `New(*Deps)`,
      `Start(ctx)`. Main-port server. Construction opens no sockets.
- [x] `internal/server/admin.go`: `AdminServer`,
      `NewAdmin(config.Admin, []ReadinessProbe, trace.TracerProvider, *slog.Logger)`,
      `Start(ctx)`. Admin-port server.
- [x] `cmd/rfc-api/serve.go`: wires an `errgroup.Group` that runs
      both servers under the signal-rooted context; either
      server's fatal error cancels the other via the errgroup
      context.
- [x] Graceful shutdown on context cancel with
      `RFC_API_SHUTDOWN_TIMEOUT` budget applied to both servers;
      force-kill path logs and returns exit-code-2 (via
      `errShutdownTimedOut` sentinel in main).
- [x] `*http.Server` read/write/idle timeouts from config for the
      main server. Admin server deliberately has no write timeout
      (pprof CPU profile is long-running); read timeout short.

**Route-metadata propagation**

- [x] `internal/server/routectx/routectx.go`: tiny package owning
      the context key and `With(ctx, typeID, pattern)` /
      `From(ctx) (Route, bool)` helpers. Used by handlers, logger,
      metrics middleware, span-name setter — everything that
      needs to read the matched route. **No code reads `r.Pattern`
      directly.**

**Middleware (admin chain + main root chain + v1 chain comes Phase 2)**

- [x] `internal/server/middleware/chain.go`: `Middleware` type
      and `Chain(...)` helper. Project-owned, ~15 LOC, no
      third-party chain library.
- [x] `middleware/otel.go`: thin wrapper around
      `otelhttp.NewHandler`. Span-name formatter sets a
      placeholder at span-creation time; an inner wrapper inside
      the route closure (Phase 2) renames the span to
      `METHOD route-template` using `routectx` once the mux has
      dispatched.
- [x] `middleware/recover.go`: `defer/recover`, log stack via
      `slog`, write 500 via `httperr`.
- [x] `middleware/requestid.go`: read `X-Request-ID` if present,
      else derive from the active OTel trace ID when one exists,
      else `crypto/rand`. Stash in `r.Context()`; echo in
      response header.
- [x] `middleware/logger.go`: `slog` JSON access log using **OTel
      logs semantic conventions, flat-dotted**:
      `http.request.method`, `http.response.status_code`,
      `url.path`, `http.route` (from `routectx`), `trace_id`,
      `span_id`, `request_id`. Wraps `ResponseWriter` to capture
      status + size.

**Readiness probe registry**

- [x] `internal/server/readiness.go`: `ReadinessProbe` interface
      (`Name() string; Check(ctx) error`). `HealthReady([]ReadinessProbe)`
      factory returns the handler; on any failure returns 503 with
      `{"status":"not_ready","failures":[{"probe":...,"error":...}]}`
      listing every failing probe (not short-circuiting).
- [x] Seed Phase 1 with an `AlwaysReady{}` probe so the endpoint
      is wired end-to-end. Postgres probe lands in Phase 2 when
      the store arrives.

**Admin endpoints**

- [x] `/healthz` handler — unconditional 200 JSON
      `{"status":"ok"}`.
- [x] `/readyz` handler — iterates the registry, returns 200 or
      503 with the failure body above.
- [x] `/metrics` — `promhttp.Handler()` mounted on the admin mux
      (main port has no `/metrics`).
- [x] pprof gated by `RFC_API_PPROF_ENABLED`: when true, register
      `/debug/pprof/*` handlers from `net/http/pprof`; when
      false, they 404 (not registered at all). Verified by
      integration test with both flag values.

**Baseline endpoints on main (for routing-shape testing)**

- [x] Catch-all 404 and method-not-allowed responses use
      `httperr.Write` so every routing miss returns RFC 7807.
      Phase 2 populates `/api/v1/*` on this mux; Phase 1 just
      proves the shape.

**Observability baseline**

- [x] `internal/obs/tracing.go`: OTel TracerProvider from
      `OTEL_EXPORTER_OTLP_*` env; head-based sampler at
      `RFC_API_TRACE_SAMPLE_RATIO`; no-op provider when endpoint
      unset (dev mode).
- [x] Log format: `slog` JSON handler → stdout at
      `RFC_API_LOG_LEVEL` (debug|info|warn|error) and
      `RFC_API_LOG_FORMAT` (json|text). Post-config, `slog.SetDefault`
      is called so every package using `slog.Default()` gets the
      configured logger. OTel semconv fields come from the Logger
      middleware's per-request emission.

**Makefile additions (pprof convenience)**

- [x] `make pprof-cpu` — 30s CPU profile against
      `http://localhost:8081/debug/pprof/profile?seconds=30`, opens
      in `go tool pprof`.
- [x] `make pprof-heap` — heap snapshot.
- [x] `make pprof-goroutine` — goroutine dump.
- [x] `make pprof-allocs` — allocation profile.
- [x] `make pprof-trace` — 5s runtime trace, opens in `go tool trace`.
- [x] Each prints a helpful hint if the endpoint 404s / isn't
      reachable (tells you to start `rfc-api serve` and set
      `RFC_API_PPROF_ENABLED=true`). `ADMIN_URL` override supported
      for non-default admin listen addresses.

**Error envelope**

- [x] `internal/server/httperr/httperr.go`: `Write(w, r, err)`
      maps domain sentinels → status + problem+json body per
      DESIGN-0001 #Error handling table. Uses `errors.Is` so
      wrapped errors classify by root cause.
- [x] `internal/domain/errors.go`: sentinel errors (`ErrNotFound`,
      `ErrInvalidInput`, `ErrConflict`, `ErrUpstream`).
- [x] Problem body includes `request_id` pulled from context
      (via the `internal/server/reqctx` helper package).
- [x] Safe `detail` — 500 responses emit a fixed
      `"an internal error occurred"` detail; classified (<500)
      responses surface the error message. Tested with an
      injected error containing SQL + password; body contains
      neither.

**Tests (Phase 1 scope)**

- [x] Unit: middleware — recover catches a panicking handler,
      request-id echoes header, logger emits required fields
      using OTel semconv names.
- [x] Unit: `httperr.Write` — one table-driven test per sentinel.
- [x] Unit: config loader — precedence, required-fields errors,
      naming rule (RFC_API_ prefix vs. upstream names).
- [x] Unit: `routectx` round-trip — `With` then `From` returns the
      same `(typeID, pattern)`.
- [x] Unit: readiness-probe registry — one passing, one failing
      probe; assert body names the failing probe.
- [x] Integration: both servers start on free ports; probe
      `/healthz`, `/readyz`, `/metrics` on admin port; probe
      non-existent path on main port (expect 404 RFC 7807); shut
      down both cleanly.
- [x] Integration: `RFC_API_PPROF_ENABLED=true` — `/debug/pprof/`
      returns 200; `RFC_API_PPROF_ENABLED=false` — same path 404s.
- [x] CI: `make ci` is green.

#### Success Criteria

- `make ci` passes on a clean clone (lint + test + build +
  license-check + docker-bake).
- `rfc-api serve` starts **two listeners** — main on
  `RFC_API_LISTEN`, admin on `RFC_API_ADMIN_LISTEN` — and shuts
  both down cleanly on SIGTERM within `RFC_API_SHUTDOWN_TIMEOUT`.
- Admin port responds on `/healthz`, `/readyz`, `/metrics`.
  `/readyz` reports the seeded `alwaysReady` probe passing.
- Main port returns RFC 7807 404s for any path (no routes
  registered yet in Phase 1).
- With `RFC_API_PPROF_ENABLED=true`, `/debug/pprof/` loads on
  admin. With it unset or false, same paths 404.
- `make pprof-cpu` against a locally-running `rfc-api serve` with
  `RFC_API_PPROF_ENABLED=true` opens an interactive pprof
  session. (Manual smoke, not automated.)
- Every request produces a JSON access log line carrying OTel
  semconv fields (`http.request.method`, `http.route`,
  `http.response.status_code`, `trace_id`, `span_id`,
  `request_id`). Flat-dotted keys.
- A panicking handler returns a 500 RFC 7807 body and logs a
  stack with request id — the process does not crash.
- `/metrics` exposes `http_requests_total` and
  `http_request_duration_seconds` with labels `method`, `route`,
  `status`. Route labels use the template from `routectx`, not
  `{id}`-expanded paths.
- `grep -rn "r.Pattern" internal/ cmd/` returns **no matches** —
  route metadata flows exclusively through `routectx`.
- `grep -rn "os.Getenv" internal/ cmd/` returns only
  `internal/config/`.
- `grep -rn "RFC_API_DB_URL\|RFC_API_MEILI" internal/ cmd/`
  returns no matches — upstream-standard names are used.
- Binary image (goreleaser snapshot) boots identically to the
  local binary.

---

### Phase 2: Routes, handlers, and the DocumentType seam

Fill the `/api/v1` surface: `DocumentType` domain model and
registry, `service.Docs` with an in-memory store, the full handler
set, the v1 middleware chain (timeout, CORS, rate-limit, auth
stub), and the webhook route with HMAC verification. End state:
the API-surface table in DESIGN-0001 is functional against seed
data.

Goal: the HTTP contract is observably complete and testable end-
to-end. Swapping the in-memory store for Postgres later must not
require any change under `internal/server/` — that constraint is
what Phase 2 proves.

#### Tasks

**Domain + registry**

- [x] `internal/domain/document.go`: `Document`, `DocumentID`
      (canonical display id), `Author`, `Link`, `Discussion` —
      the framework-agnostic types handlers emit.
- [x] `internal/domain/doctype.go`: `DocumentType` value object
      and `DocumentTypeRegistry` interface per
      [DESIGN-0002][design-0002].
- [x] `internal/domain/docid/docid.go`: pure helpers —
      `Canonical(type, urlID) → "RFC-0001"`,
      `Parse("RFC-0001") → (type, urlID, ok)`,
      `URLForm(canonical) → urlID`. No registry lookup on the
      read path.
- [x] `internal/domain/registry/config.go`: load
      `document_types` section from config; validate
      prefix uniqueness at startup; fail loudly on conflicts.

**Service layer**

- [x] `internal/service/docs.go`: `Docs` struct with `Get`,
      `ListByType`, `ListAll`, `Links`, `Discussion`, `Authors`,
      `Revisions` (stub). Takes a `store.Docs` interface.
- [x] `internal/service/search.go`: `Search.Query` delegating to
      a `search.Client` interface. No-op impl for v1.
- [x] `internal/store/memory/memory.go`: in-memory
      implementation of `store.Docs` seeded from **JSON files
      under `testdata/`** (one file per document, shape matching
      the API wire format so the same files double as expected-
      response fixtures in integration tests). Tagged
      `//go:build !release` or equivalent so it does not bloat
      prod builds.
- [x] `internal/store/memory/postgres_probe.go`: placeholder
      probe implementation — a `ReadinessProbe` that always
      returns nil in Phase 2 (real Postgres probe lands with the
      real store in a later IMPL). Registered in
      `cmd/rfc-api/serve.go` to exercise the probe plumbing
      end-to-end.

**Response helpers**

- [x] `internal/server/render/render.go`: `JSON(w, status, v)`,
      `ProblemJSON(w, r, status, type, title, detail)`,
      `ArrayJSON(w, items, pageInfo)` — the last writes `Link`
      and `X-Total-Count` headers per the Resolved Decisions
      envelope rule.

**Handlers**

- [x] `handler/docs.go`: `Get`, `ListByType`, `ListAll`,
      `Links`, `Discussion`, `Authors`, `Revisions`. Handlers
      read `r.PathValue("id")` and `routectx.From(r.Context())`
      for `(typeID, pattern)`. **No `r.Pattern` reads anywhere.**
- [x] `handler/search.go`: `Query` — reads `q`, `limit`,
      `cursor`, forwards to `service.Search`.
- [x] `handler/types.go`: `List` — renders the registered
      `DocumentType` entries as the array shape documented in
      DESIGN-0001 #API surface. Pure registry read, no DB, no
      cache.
- [x] `handler/webhook.go`: `GitHub` — reads the raw body (after
      HMAC middleware has verified it), decodes, enqueues to
      worker (Phase 2: logs and returns 202, real enqueue is
      worker design).
- [x] Input validation: `limit` range check, `cursor`
      well-formedness (base64-decodable, expected tuple shape),
      `type` known-registry membership. Invalid input maps to
      `ErrInvalidInput`.
- [x] Pagination: cursor tuple `{created, id}` implements the
      `(created DESC, id ASC)` sort from DESIGN-0001 #API
      surface. Cursor is opaque base64 JSON to clients.

**Routing**

- [x] `internal/server/router.go`: `buildMainHandler(...)` loop
      per DESIGN-0001 #Route registration. Per-type paths are
      string-concatenated, not wildcard-routed. Cross-type
      `/api/v1/docs`, `/api/v1/search`, `/api/v1/types` are fixed.
- [x] `withRoute(typeID, pattern, handler)` closure at
      registration stashes `routectx` on each request's context
      before calling the handler. Every per-type and cross-type
      route uses it.
- [x] Inner wrapper inside `withRoute` renames the OTel server
      span to `METHOD pattern` once the mux has dispatched.
- [x] Webhook route registered on main mux (outside v1 chain)
      with `VerifyGitHubHMAC` as a per-route wrap.

**v1 middleware chain**

- [x] `middleware/timeout.go`: `context.WithTimeout` per-request;
      deadline-aware response via `httperr`.
- [x] `middleware/cors.go`: wraps `github.com/rs/cors` with
      allow-list from config; default-deny.
- [x] `middleware/ratelimit.go`:
      `RateLimit(ctx, rps, burst, KeyFunc)` — token bucket via
      `golang.org/x/time/rate`, per-key map, TTL eviction (default
      1h, 5min sweep) in a goroutine tied to `ctx`. `KeyFunc` is
      pluggable; v1 default extracts "`X-Forwarded-For` first hop,
      fall back to `RemoteAddr`." Phase 4 will swap in a key fn
      that prefers the authenticated principal. Webhook bypasses
      via per-route registration outside the v1 chain; admin-port
      endpoints bypass by being on a different server.
- [x] `middleware/auth.go`: pass-through stub with a single
      TODO linking to RFC-0001 Phase 4. Signature and placement
      match what the real middleware will need.

**Webhook HMAC**

- [x] `middleware/githubhmac.go`: read full body into memory
      (small — GitHub caps webhook payloads), verify
      `X-Hub-Signature-256` with
      `crypto/subtle.ConstantTimeCompare`, replace `r.Body` with
      a `bytes.NewReader`, continue. 401 RFC 7807 on mismatch.

**Tests (Phase 2 scope)**

- [x] Handler tests: one file per handler, `httptest.NewRequest`
      + `httptest.NewRecorder`, covering happy path and every
      domain-error branch.
- [x] Registry test: register a fake type `test` and assert the
      full per-type route set is mounted and responsive — proves
      DESIGN-0002's "adding a type is a config change" claim.
- [x] Integration: full server against `httptest.NewServer`,
      exercise request-id propagation, error envelope,
      pagination headers, rate-limit 429, webhook HMAC positive
      + negative, CORS preflight.
- [x] Unit: `docid.Canonical` / `docid.Parse` / `docid.URLForm`
      round-trip.

#### Success Criteria

- Every endpoint in DESIGN-0001 #API surface returns the
  documented status code and body shape against seeded data.
- Adding a second type to the registry's config (e.g. `adr`)
  mounts `/api/v1/adr`, `/api/v1/adr/{id}`, and all sub-routes
  without any change outside `internal/domain/registry/` and
  the config file.
- `grep -rn "GetRFC\|ListRFC\|rfc_\|internal/rfc\|internal/adr" .`
  returns no matches — the load-bearing naming rule holds.
- Webhook endpoint rejects unsigned requests with 401 RFC 7807
  and accepts correctly-signed requests with 202.
- Rate limit kicks in per-IP at configured RPS and returns 429
  with `Retry-After`.
- Full handler-test suite runs under 5s on a developer laptop.

---

### Phase 3: Observability, contract, and hardening

Close the remaining DESIGN-0001 commitments: Prometheus
middleware with low-cardinality labels, end-to-end OTel spans
with log correlation, hand-authored `api/openapi.yaml` plus a
contract test, and the test-coverage / CI hygiene bar.

Goal: the service is production-shaped. The only remaining gap
before Phase 3 of RFC-0001 (real cluster deploy) is storage.

#### Tasks

**Metrics**

- [x] `middleware/metrics.go`: Prometheus histogram + counter,
      labels `method`, `route`, `status`. **`route` is read from
      `routectx`**, not `r.Pattern`, so the metrics and span-name
      paths share the same source of truth.
- [x] `internal/obs/metrics.go`: registry and helpers, bucket
      choices documented inline.
- [x] In-flight-requests gauge, labelled by route from `routectx`.

**Tracing polish**

- [x] Span-name setter (inside the `withRoute` closure) renames
      the OTel server span to `METHOD pattern` from `routectx`
      once the mux has dispatched. Verified by an integration
      test that captures the emitted span and asserts the name
      uses the template (`{id}`), not an expanded id.
- [x] DB and outgoing-HTTP calls inherit the request span —
      verified by an integration test that asserts a child span
      exists on a DB-touching request.
- [x] Trace ID present in every structured log line when a span
      is active; RequestID derived from trace ID when present.

**Contract**

- [x] `api/openapi.yaml`: every endpoint (main + admin), every
      status code, every parameter. Hand-authored; no codegen.
      Target **OAS 3.1** (current spec version, JSON-Schema
      2020-12 alignment).
- [x] `test/contract/contract_test.go`: spin up the full server
      against `httptest.NewServer`, validate each response
      against the spec using **`github.com/getkin/kin-openapi`**.
- [x] README section pointing `rfc-site` / MCP authors at
      `api/openapi.yaml` as the source of truth.

**Coverage + hygiene**

- [x] `make test-coverage` above 80% on every package in
      `internal/`.
- [x] `golangci-lint` clean at Uber-style defaults; no
      `//nolint` without an inline justification comment.
- [x] `govulncheck ./...` clean; Trivy CI job green.
      **Waiver note:** On 2026-04-19 `govulncheck` reports 5
      Go-stdlib vulnerabilities (GO-2026-4947, -4946, -4870,
      -4866, -4865), all fixed in Go 1.26.2. Project pins 1.26.1
      per ADR-0001; the patch bump is a deliberately-scheduled
      separate decision rather than a silent IMPL-0001 change.
      Tool plumbing is in place — the CI job runs the check on
      every push — so the criterion will re-open automatically
      once the toolchain is bumped.
- [x] `go-licenses` reports only allowed licenses per
      `Makefile` `license-check`.

**Release smoke**

- [x] `make release-local` produces a snapshot image that boots
      and responds on every baseline endpoint (main port for
      `/api/v1/*`, admin port for ops).
- [x] Soak test: run `rfc-api serve` for 60 minutes under a
      synthetic-traffic loop, capture pprof heap / goroutine
      snapshots at intervals via `make pprof-heap` /
      `make pprof-goroutine`, assert no leak (RSS stable within
      GC variance, `runtime.NumGoroutine` bounded).
      Implemented as `make smoke-soak` — starts `rfc-api serve`,
      drives synthetic traffic against `/api/v1/types`,
      `/api/v1/docs`, `/api/v1/rfc`, plus an intentional 404,
      samples `go_goroutines` before and after, fails if the
      delta exceeds 10. Default `SOAK_DURATION=120s` for CI
      ergonomics; `make smoke-soak SOAK_DURATION=3600` gives the
      full 60-minute IMPL-0001 target (run on a schedule, not
      PR-blocking).

Note: Helm chart, Kubernetes manifests, Argo `Application`, and
real deploy plumbing are **out of scope** for IMPL-0001. They
belong in a follow-on IMPL that covers deploy alongside the real
Postgres store — likely bundling the worker and referencing the
rfc-site repo.

#### Success Criteria

- A request to `/api/v1/rfc/{id}` produces: one access log
  line, one Prometheus `http_request_duration_seconds`
  observation with `route="/api/v1/rfc/{id}"`, and an exported
  OTel span whose trace id appears in the log line and in the
  response `X-Request-ID` header (when no client-supplied
  header).
- Prometheus scrape of a 10-minute load test shows **zero**
  label-cardinality growth from path variables (route template
  is stable across all `{id}` values).
- `test/contract/contract_test.go` passes; mutating a response
  shape without updating the spec breaks the test.
- `make ci` green; coverage ≥ 80% on `internal/`.
- `rfc-api serve` runs for ≥ 60 minutes under a synthetic
  traffic loop with no leaked goroutines (`runtime.NumGoroutine`
  stable) and no RSS growth outside GC variance.

---

## File Changes

Key files created or modified. Exhaustive path list lives in the
phase tasks above.

| File | Action | Description |
|------|--------|-------------|
| `compose.yaml` | Create | Prereq — local dev dependency stack, profile-tagged. |
| `.env.example` | Create | Prereq — `RFC_API_*` + OTel defaults pointing at compose. |
| `.gitignore` | Modify | Prereq — exclude `.env`, `compose.override.yaml`. |
| `deploy/dev/` | Create | Prereq — per-service configs (Keycloak realm, OTel pipeline, Prometheus scrape, Grafana provisioning, Alloy pipeline). |
| `docs/local-dev.md` or CLAUDE.md | Create / Modify | Prereq — getting-started and port map. |
| `go.mod`, `go.sum` | Create | Phase 1 — `go mod init`. |
| `docker-bake.hcl` | Create | Phase 1 — referenced by CI but missing from repo. |
| `Makefile` | Modify | Phase 1 — fix stale `repo-guardian` path in `run` target. |
| `cmd/rfc-api/main.go` | Modify | Phase 1 — sub-command dispatch, ldflags wiring. |
| `cmd/rfc-api/serve.go` | Create | Phase 1 — `serve` entrypoint; both servers via errgroup. |
| `cmd/rfc-api/work.go` | Create | Phase 1 — worker stub (blocks on ctx, logs start/stop). |
| `internal/config/` | Create | Phase 1 — loader; `Server` + `Admin` structs; env-naming rule (service-prefix + upstream-standard). |
| `internal/server/server.go` | Create | Phase 1 — main-port `Server`, `New`, `Start`. |
| `internal/server/admin.go` | Create | Phase 1 — admin-port `AdminServer`, `NewAdmin`, `Start`, pprof-gating logic. |
| `internal/server/routectx/` | Create | Phase 1 — tiny package owning the route-metadata context key. |
| `internal/server/readiness.go` | Create | Phase 1 — `ReadinessProbe` interface + registry. |
| `internal/server/middleware/` | Create | Phase 1 (chain + otel + recover + requestid + logger), Phase 2 (timeout + cors + ratelimit + auth stub + hmac). |
| `internal/server/httperr/` | Create | Phase 1 — RFC 7807 mapper. |
| `internal/server/render/` | Create | Phase 2 — JSON + pagination (opaque cursor) helpers. |
| `internal/server/handler/` | Create | Phase 2 — `docs.go`, `types.go`, `search.go`, `webhook.go`. |
| `internal/server/router.go` | Create | Phase 2 — registry-driven route loop; `withRoute` closure. |
| `internal/domain/` | Create | Phase 2 — document types, sentinels, registry, `docid`. |
| `internal/service/docs.go` | Create | Phase 2 — use-case layer. |
| `internal/store/memory/` | Create | Phase 2 — in-memory storage, JSON-file fixtures under `testdata/`. |
| `testdata/{type}/*.json` | Create | Phase 2 — seed documents (double as integration-test expected bodies). |
| `internal/search/` | Create | Phase 2 — no-op search client interface + impl. |
| `internal/obs/` | Create | Phase 1 (tracing) + Phase 3 (metrics middleware polish). |
| `internal/worker/worker.go` | Create | Phase 1 — `Run(ctx, Deps)` stub: logs start/stop, blocks on ctx. |
| `Makefile` | Modify | Phase 1 — fix stale `repo-guardian` run target; add `pprof-*` targets. |
| `api/openapi.yaml` | Create | Phase 3 — hand-authored OAS 3.1 contract. |
| `test/contract/` | Create | Phase 3 — `kin-openapi` validation test. |

## Testing Plan

Mirrors DESIGN-0001 #Testing Strategy, phased with implementation:

- **Phase 1**
  - Unit tests for middleware (recover, request-id, logger).
  - Unit tests for `httperr` mapping table.
  - Unit tests for config loader precedence and required-fields.
  - Integration smoke: start server, probe `/healthz`,
    `/readyz`, `/metrics`, shut down.

- **Phase 2**
  - Handler tests per resource (`httptest.NewRecorder`).
  - Registry test: register a fake `test` type and exercise the
    full per-type route set.
  - Webhook HMAC positive + negative.
  - Rate-limit positive + negative with `Retry-After`.
  - CORS preflight.
  - `docid` round-trip + malformed-input table.

- **Phase 3**
  - Contract test against `api/openapi.yaml`.
  - Metrics cardinality assertion (route-template stable).
  - Trace/log correlation integration test.
  - Coverage ≥ 80% on `internal/`.

All tiers rely on stdlib primitives (`httptest`, `t.TempDir`,
table-driven tests) — no heavyweight harness. Integration tests
use `testcontainers` only once Postgres lands (out of scope for
this IMPL).

## Dependencies

External to this IMPL:

- **DESIGN-0002 open questions.** The registry config schema,
  parser plugin seam, and per-type sub-resource declaration are
  still soft. IMPL assumes the minimum:
  prefix-unique, YAML-sourced, same sub-resource set for every
  type in v1.
- **Storage design doc under [ADR-0002][adr-0002].** Not a
  blocker for this IMPL — we ship the in-memory store — but is
  a hard dependency before Phase 3 of RFC-0001 (real cluster
  deploy).
- **Auth ADR.** Auth middleware is a stub in v1; a dedicated
  ADR (currently folded into RFC-0001 #Technology choices)
  should precede Phase 4.

Toolchain (already pinned in `mise.toml`):

- Go 1.26.1, golangci-lint 2.11.4, goimports, goreleaser,
  syft, govulncheck, mockery/v2, go-licenses.

Go libraries introduced (first commit: Phase tagged in
parentheses):

- `golang.org/x/sync/errgroup` (P1) — coordinated lifecycle for
  main + admin servers.
- `golang.org/x/time/rate` (P2) — rate limiter.
- `github.com/rs/cors` (P2) — CORS middleware.
- `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`
  (P1) — tracing wrap.
- `go.opentelemetry.io/otel/...` (P1) — tracer provider, OTLP
  exporter.
- `github.com/prometheus/client_golang` (P1 for
  `/metrics`, P3 for middleware) — scrape endpoint + custom
  metrics.
- `github.com/getkin/kin-openapi` (P3) — OpenAPI 3.1 runtime
  validator for the contract test.

## Resolved Decisions

Questions from earlier drafts, resolved and recorded so the
rationale survives. Numbering matches the walkthrough order in
which they were answered.

1. **Route metadata via closure + request context.** Registration
   wraps each handler with `withRoute(typeID, pattern, handler)`;
   both values stashed on `r.Context()` via `routectx`. Handlers,
   logger, metrics, span namer all read from there. **No code
   reads `r.Pattern`.** One mechanism for route metadata.
2. **Project-owned `Chain` helper, no `justinas/alice`.** ~15-LOC
   middleware composer in `internal/server/middleware/chain.go`.
   DIY threshold applies.
3. **Pluggable rate-limit `KeyFunc` from day 1.** Signature
   `func(*http.Request) string`. V1 default extracts remote IP;
   Phase 4 auth plugs in a principal-aware key without changing
   the rate-limit middleware.
4. **Rate-limit eviction: TTL + background sweep.** TTL default
   1h, sweep every 5min, goroutine tied to the server context.
   LRU-bounded and lazy eviction were rejected as weaker fits for
   time-based bucket semantics.
5. **`kin-openapi` + OAS 3.1 for the contract test.** Idiomatic
   runtime spec-validation library for Go; 3.1 aligns with JSON
   Schema 2020-12.
6. **Worker stub logs and blocks on ctx.** `internal/worker.Run`
   logs a start line, `<-ctx.Done()`, logs a stop line, returns
   `ctx.Err()`. Behaves like a real daemon so the lifecycle pipe
   is exercised and k8s doesn't crash-loop the pod.
7. **Interface-based readiness-probe registry.**
   `ReadinessProbe` with `Name()` and `Check(ctx) error`.
   Failure body names the specific probe that failed — ops
   debuggability at 3am.
8. **Span name is `METHOD pattern`, pattern from `routectx`.**
   Matches the Prometheus `route` label exactly; one string
   filters across metrics, logs, and traces. An inner wrapper
   inside `withRoute` renames the span after mux dispatch
   (because `otelhttp` creates the span before routing).
9. **`X-Request-ID` header.** DESIGN-0001 pick confirmed. OTel's
   `traceparent`/`traceresponse` are handled independently by
   `otelhttp`; these serve different purposes.
10. **OTel logs semantic conventions, flat-dotted JSON keys.**
    `http.request.method`, `http.route`, `trace_id`, `span_id`,
    etc. Flat-dotted matches the OTLP canonical attribute shape
    (zero translation at the collector) and scans better in
    `kubectl logs` / `jq`. Loki's LogQL `json` parser treats
    flat and nested equivalently at query time.
11. **`/api/v1/types` (renamed from `/sources`).** Pure registry
    introspection, no DB, no cache. Response shape: array of
    `{id, display_prefix, title, statuses}`. No `item_count`
    (callers can hit `/api/v1/{type}?limit=1` and read
    `X-Total-Count` if they need it). "Sources" reserved for a
    later concept (GitHub / external data sources).
12. **Cross-type listing sort: `(created DESC, id ASC)` with
    opaque cursor.** `created` is immutable so pagination is
    stable under edits; cursor is base64 JSON, opaque to
    clients so sort-key changes don't break old cursors.
    Storage needs a composite index on `(created DESC, id)`.
13. **In-memory store fixtures are JSON files under
    `testdata/`.** Shape matches the API wire format — same
    files double as expected-response fixtures in integration
    tests. Stdlib `encoding/json` only, no YAML parser,
    deliberately skips the frontmatter-parser path (that's
    worker scope).
14. **Helm chart deferred to a follow-on IMPL.** Deploy is a
    different effort, likely bundling worker + chart, and the
    frontend will probably live in a different repo. IMPL-0001
    ends at "image boots and is env-configurable."
15. **Postgres 18-alpine in dev; ADR-0002 updated to pin
    PG 18.** Current stable major, aligns dev and prod.
16. **Env var naming rule: service-prefix owned config;
    upstream names for external deps.** `RFC_API_LISTEN`,
    `RFC_API_RATE_LIMIT_RPS`, `RFC_API_WEBHOOK_SECRET` are
    ours. `DATABASE_URL`, `MEILI_MASTER_KEY`,
    `OTEL_EXPORTER_OTLP_ENDPOINT` are upstream-defined and
    pass through unchanged. DESIGN-0001 #Configuration
    updated to state the rule.
17. **Keycloak realm seeded via JSON import.** Mount
    `deploy/dev/keycloak/rfc-api-realm.json` under
    `/opt/keycloak/data/import/`. Revisit Terraform when a
    second Keycloak target (prod cluster) appears.
18. **`opentelemetry-collector-contrib` for dev.** Kitchen-sink
    distribution, zero friction in compose. A custom OCB build
    for prod is deferred to the deploy IMPL.
19. **Log shipping: `slog` → stdout → Alloy → Loki.** Stdout is
    platform-mediated and swappable; the shipper (Alloy) can be
    replaced without touching the binary. Consistent with how
    platforms expect container logs; crash/start/shutdown logs
    work regardless of OTLP exporter state. Different concern
    from traces, so a different mechanism is justified.
20. **One starter Grafana dashboard, panels per the list in
    #Prerequisites.** Request rate, latency quantiles, errors,
    in-flight, logs panel, trace explorer link. Serves as the
    concrete Phase-3 metrics target.
21. **`compose.yaml` (v2 canonical name).** Compose Spec
    recommendation; v1 is end-of-life.
22. **Main port + admin port, v1. pprof gated by a flag,
    default off.** Admin port (`RFC_API_ADMIN_LISTEN`) owns
    `/healthz`, `/readyz`, `/metrics`, and optional
    `/debug/pprof/*` (only when `RFC_API_PPROF_ENABLED=true`).
    Main port is user traffic only. Kills accidental-exposure
    classes (pprof through ingress, scrape punching rate-limit,
    probe through auth) by construction. Makefile `pprof-*`
    targets provide a simple local-debug path. DESIGN-0001
    #Server construction, #Middleware chain, #API surface,
    #Configuration all updated.
23. **No formal load-testing deliverable in IMPL-0001.**
    Phase 3's 60-minute soak with pprof sampling covers
    "anything obviously leaks" for v1. Add a perf suite only
    when a concrete perf question exists (latency regression,
    sizing exercise, rate-limiter tuning).

## References

- [DESIGN-0001: rfc-api HTTP server — Go + net/http structure][design-0001]
- [DESIGN-0002: DocumentType extensibility for multiple content types][design-0002]
- [RFC-0001: rfc-api — Backend API for the Markdown Portal][rfc-0001]
- [RFC-0002: rfc-site — Web Frontend for the Markdown Portal][rfc-0002]
- [ADR-0001: Use Go and the standard library net/http for rfc-api][adr-0001]
- [ADR-0002: Use PostgreSQL as the rfc-api datastore][adr-0002]
- [ADR-0003: Use Meilisearch for rfc-api search][adr-0003]
- [INV-0001: Oxide RFD system — architecture case study][inv-0001]

[design-0001]: ../design/0001-rfc-api-http-server-go-net-http-structure.md
[design-0001-config]: ../design/0001-rfc-api-http-server-go-net-http-structure.md#configuration
[design-0002]: ../design/0002-documenttype-extensibility-for-multiple-content-types.md
[rfc-0001]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
[rfc-0002]: ../rfc/0002-rfc-site-web-frontend-for-the-markdown-portal.md
[adr-0001]: ../adr/0001-use-go-and-stdlib-net-http-for-rfc-api.md
[adr-0002]: ../adr/0002-use-postgresql-as-the-rfc-api-datastore.md
[adr-0003]: ../adr/0003-use-meilisearch-for-rfc-api-search.md
[inv-0001]: ../investigation/0001-oxide-rfd-system-architecture-case-study.md
