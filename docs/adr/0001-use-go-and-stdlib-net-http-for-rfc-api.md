---
id: ADR-0001
title: "Use Go and the standard library net/http for rfc-api"
status: Accepted
author: Donald Gifford
created: 2026-04-18
accepted: 2026-04-20
---
<!-- markdownlint-disable-file MD025 MD041 -->

# 0001. Use Go and the standard library net/http for rfc-api

<!--toc:start-->
- [Status](#status)
- [Context](#context)
- [Decision](#decision)
- [Consequences](#consequences)
  - [Positive](#positive)
  - [Negative](#negative)
  - [Neutral](#neutral)
- [Alternatives Considered](#alternatives-considered)
- [References](#references)
<!--toc:end-->

## Status

Accepted — implementation landed in IMPL-0001; the server runs entirely
on `net/http` as specified.

## Context

[RFC-0001: rfc-api][rfc-0001] proposes a long-running backend service
that syncs Markdown documents from GitHub, persists them, and serves
them as a read-only JSON HTTP API to the `rfc-site` frontend, an MCP
server, and other programmatic consumers. The service is deployed to
our existing Kubernetes cluster via Helm and Argo.

The service is read-heavy, I/O-bound, needs good concurrency for the
sync worker running alongside the HTTP server, and must produce a
single static binary that fits cleanly into our existing container and
release tooling. It should not introduce a new language or runtime to
the platform.

The HTTP surface is small — about eight endpoints in v1 per
[RFC-0001 #API surface][rfc-0001-api]. All responses are JSON. The
middleware set we need is well-bounded: recover, request id, logging,
tracing, timeout, CORS, rate limiting, and (in a later phase) auth.

A language and HTTP layer decision needs to be recorded before any
implementation begins because it shapes the rest of the stack (driver,
observability libraries, CI image base, release pipeline) and because
downstream ADRs (datastore, search) assume this choice.

## Decision

Build `rfc-api` in **Go 1.26.1** using the **standard library's
`net/http` package** — specifically `net/http.ServeMux` with the
Go 1.22+ pattern-based routing (`"GET /docs/{id}"` style) — and
decorator-pattern middleware. No third-party HTTP framework.

- Go is the language for backend services in this repo. The project
  is already wired for Go: `go.mod`-driven CI, a Go-first Makefile,
  Uber-style `golangci-lint`, `goreleaser`-based binary and multi-arch
  release, and a Go-oriented toolchain pinned in `mise.toml`. We adopt
  that baseline.
- `net/http.ServeMux` is the router. Go 1.22 added method-aware
  patterns with path parameters (`"GET /docs/{id}"` and
  `r.PathValue("id")`), which covers the routing needs for this
  service without pulling in a framework.
- Middleware is the standard decorator pattern —
  `func(http.Handler) http.Handler` — with a small, project-owned
  middleware set composed once at server construction.
- Handlers use the standard `http.HandlerFunc` signature; request
  bodies are decoded with `encoding/json`; input validation uses a
  small helper (library choice deferred to the design doc).
- The specific Go toolchain version is pinned to `1.26.1` to match
  the repository's existing `mise.toml` pin; upgrades are made
  repo-wide, not per-service.

This ADR does not prescribe package layout, dependency-injection
style, specific middleware implementations, or validation library
choice — those live in the `rfc-api` HTTP-server design doc.

## Consequences

### Positive

- **Zero framework lock-in.** `http.Handler` and `http.HandlerFunc`
  are Go's universal HTTP abstraction. Every third-party library
  — OpenTelemetry, Prometheus, logging middleware, auth libraries —
  targets `net/http` first. There is no adapter layer and no
  framework-specific handler signature leaking into tests or tooling.
- **Stability.** `net/http` has not broken compatibility in over a
  decade and is covered by Go's compatibility promise. Echo, Gin,
  and other frameworks have historically had major-version migrations
  (Echo v3 → v4 → v5); this service will outlive any of those
  migrations.
- **Smaller dependency surface.** One fewer dependency tree for
  govulncheck and Trivy to scan, patch, and report on. Combined with
  the existing license-check discipline, every dependency we avoid
  is one less thing to maintain.
- **First-class observability integration.** OpenTelemetry's Go
  instrumentation (`otelhttp`) wraps an `http.Handler` directly, and
  Prometheus metrics middleware for `net/http` is well-trodden.
  Framework-wrapped equivalents are one indirection removed and
  typically third-party.
- **Onboarding.** Engineers new to the repo do not need to learn a
  framework's idioms on top of Go. Reviewers read `w`, `r`,
  `http.Handler` — exactly what they already know.
- **Concurrency model fits the workload.** The sync worker, webhook
  handler, and HTTP server can coexist in one process using
  goroutines without reaching for a worker/broker split.
- **Single static binary** that fits the existing `goreleaser` +
  Docker Bake release flow and the existing Helm chart pattern for
  Go services.

### Negative

- **Middleware shelf is DIY.** The ~8 cross-cutting middlewares we
  need (recover, request id, logger, tracing, timeout, CORS, rate
  limit, auth) are each ~10–30 lines we write and own. A framework
  provides these one-liner-ready. The cost is small and one-time,
  but real.
- **No built-in binding/validation ergonomics.** Handlers do
  `json.Decode` + a validation helper rather than a single `Bind`
  call. A thin helper can hide the boilerplate, but it is one more
  project-owned piece of code.
- **No native route groups.** `ServeMux` does not have Echo's
  `e.Group("/api/v1", mw...)` primitive. We implement the equivalent
  as a small convention (a helper that applies a middleware stack to
  a set of routes registered under a prefix). Low cost; different
  shape from other frameworks some of us have used.
- **Team familiarity bias.** Some of us have reached for a framework
  by reflex on past projects. Using `net/http` for this service is a
  small deliberate re-orientation.

### Neutral

- **Version pinning is repo-wide.** Upgrading Go is a coordinated
  change across services, not a per-service decision. No separate
  framework version to coordinate.
- **No opinion on middleware implementations here.** Which request-id
  generator, logger, rate-limiter, or tracing wrapper is used is a
  design-doc decision, not an ADR-level one.
- **Option to adopt a framework later remains open.** Because
  `http.Handler` is universal, any future decision to introduce a
  framework is a focused rewrite of the HTTP seam, not a
  cross-cutting refactor of business logic.

## Alternatives Considered

1. **Echo v5.** An earlier version of this ADR selected Echo v5 on
   ergonomics and team familiarity grounds. Reconsidered and
   rejected before any implementation began, on the grounds that:
   (a) Go 1.22+ pattern-based `ServeMux` routing closes most of the
   historical gap that motivated reaching for Echo in the first
   place; (b) Echo v5 is not yet released stable and pinning a
   greenfield service to a beta major feels premature for a service
   intended to live for years; (c) `net/http` is the universal
   target for the Go HTTP ecosystem — OTel, Prometheus, auth
   libraries, and middleware all target `net/http` first and
   framework-wrap second; (d) every framework dependency avoided is
   one less thing for govulncheck and Trivy to scan and patch. Team
   familiarity with Echo is a one-time cost; framework lock-in is a
   recurring one.
2. **Chi.** A close second if a framework were warranted. Stays
   aligned with `net/http` semantics, minimal, mature, and
   well-documented. Rejected on the narrower grounds that if we are
   reaching for something close to stdlib, we might as well use
   stdlib itself and keep the dependency surface minimal. Revisit
   if routing ergonomics or middleware composition becomes a real
   pain point; migrating to Chi from stdlib is a small, localized
   change.
3. **Gin.** More opinionated, larger surface, looser `net/http`
   alignment. No advantages over `net/http` for this workload.
4. **Fiber.** Built on `fasthttp`, not `net/http`. Incompatible with
   the `net/http` middleware and instrumentation ecosystem we intend
   to use. Rejected.
5. **A non-Go language (Rust, TypeScript/Node, Python).** Rejected.
   Each would introduce a new runtime, new CI/release tooling, and
   a new security-scanning posture for marginal or negative payoff
   on this workload.

## References

- [RFC-0001: rfc-api — Backend API for the Markdown Portal][rfc-0001]
- [RFC-0011: Markdown Portal][rfc-0011]
- [Go `net/http`](https://pkg.go.dev/net/http)
- [Go 1.22 release notes — enhanced ServeMux patterns](https://go.dev/blog/routing-enhancements)
- [`otelhttp`](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp)
- Repo toolchain baseline: `mise.toml`, `.golangci.yml`,
  `.goreleaser.yml`, `Makefile`.

[rfc-0001]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
[rfc-0001-api]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md#api-surface-indicative
[rfc-0011]: ../../INGEST_RFC.md
