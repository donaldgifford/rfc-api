# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project state

Phase 1–3 of [IMPL-0001][impl-0001] are complete. The HTTP server boots end-to-end:

- `cmd/rfc-api/` — `serve` + `work` subcommands under a small dispatcher; `version` / `help`; signal-rooted ctx; errgroup lifecycle for both servers.
- `internal/server/` — main + admin servers, registry-driven `/api/v1/{type}/*` router with cross-type `/docs` / `/search` / `/types`, full middleware chain (OTel → recover → request-id → logger → metrics on root; timeout → CORS → rate-limit → auth-stub on v1), RFC 7807 problem+json error envelope, per-route GitHub webhook with HMAC verification.
- `internal/domain/` — framework-agnostic `Document`, `DocumentType`, registry (prefix + id uniqueness enforced at load), `docid` pure helpers.
- `internal/service/` + `internal/store/memory/` — service layer + in-memory store (JSON seed). `internal/search` ships a `NoopClient` for v1; Meilisearch lands later.
- `internal/obs/` — OTel TracerProvider (OTLP/gRPC when `OTEL_EXPORTER_OTLP_ENDPOINT` is set, no-op otherwise), Prometheus registry + HTTP collectors.
- `api/openapi.yaml` — hand-authored OAS 3.1. `test/contract/` validates every handler against it via `kin-openapi` on every CI run.

[impl-0001]: ./docs/impl/0001-rfc-api-http-server-phase-1-implementation.md

## Local development

See [`docs/local-dev.md`](./docs/local-dev.md) for the runbook (getting started, port map, compose profiles, pprof workflow, troubleshooting). TL;DR: `mise install && cp .env.example .env && make compose-up && go run ./cmd/rfc-api serve`.

Dev deps run in `docker compose` via profile-tagged services (`postgres`, `meilisearch` default; `keycloak`, `otel-collector`, `jaeger`, `prometheus`, `grafana`, `loki`, `alloy` opt-in). The `rfc-api` binary itself is **never** run inside compose — always host-run via `go run` or `make run-local`. `docker build` is reserved for goreleaser / CI / release.

## Commands

All workflows go through the `Makefile`. Run `make help` for the full list.

- `make build` — builds `build/bin/rfc-api` from `./cmd/rfc-api` with `-ldflags` injecting `main.version` / `main.commit`.
- `make test` — `go test -v -race ./...`
- `make test-pkg PKG=./pkg/foo` — run a single package.
- `make test-coverage` / `make test-report` — coverage out to `coverage.out`; `test-report` opens HTML.
- `make lint` / `make lint-fix` — golangci-lint (config is Uber-style based, see `.golangci.yml`).
- `make fmt` — `gofmt -s -w .` + `goimports -local github.com/donaldgifford`.
- `make check` — quick pre-commit: lint + test.
- `make ci` — full CI locally: lint + test + build + license-check.
- `make license-check` — allowed licenses: Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, ISC, MPL-2.0.
- `make release-local` — `goreleaser --snapshot --clean --skip=publish --skip=sign`.
- `make release TAG=vX.Y.Z` — tags and pushes; the `release.yml` workflow does the rest.
- `make smoke` — CLI smoke tests (help / version / unknown / serve / work).
- `make smoke-soak` — synthetic-traffic soak with goroutine-leak check; default `SOAK_DURATION=120` s, set `SOAK_DURATION=3600` for the 60-minute IMPL-0001 target.
- `make compose-up` / `compose-up-auth` / `compose-up-obs` / `compose-up-full` — bring up dev dep profiles.
- `make pprof-cpu` / `pprof-heap` / `pprof-goroutine` / `pprof-allocs` / `pprof-trace` — grab pprof against the admin port (set `ADMIN_URL=` to override).

Note: `make run` builds and runs the binary (`$(BIN_DIR)/$(PROJECT_NAME)`). `make run-local` does the same thing via a separate target — the two are effectively aliases at this point; consolidate later if the distinction is never used.

## Toolchain

Managed by `mise` via `mise.toml`. Pinned versions that matter:

- Go **1.26.1**
- golangci-lint **2.11.4** (CI pins the same)
- `docz` (donaldgifford/docz) for structured docs
- `goimports`, `mockery/v2`, `go-licenses`, `govulncheck`, `syft`, `goreleaser` (via release workflow)
- Markdown/YAML: `markdownlint-cli2`, `yamlfmt`, `yamllint`, `prettier`, `makefmt`, `checkmake`

Run `mise install` to materialize the toolchain before running `make` targets.

## Documentation system

Docs live in `docs/{rfc,adr,design,impl,plan,investigation}/` and are managed by **docz** (config in `.docz.yaml`). Each type has its own ID prefix (RFC, ADR, DESIGN, IMPL, PLAN, INV), width (4), and status lifecycle — use `docz create` rather than hand-authoring to keep indexes and IDs consistent. `index.auto_update: true` means `docz create` rewrites the type-level `README.md` indexes automatically.

The `wiki` section of `.docz.yaml` drives `mkdocs.yml` (Backstage TechDocs / `techdocs-core` plugin). Prefer editing `.docz.yaml` + running `docz wiki` over hand-editing `mkdocs.yml`.

## Architecture docs (read before writing code)

The repo has a full architecture doc set in place before any Go code exists. Treat these as authoritative — implementation flows from them, not the other way around. `docz list` shows the current set at a glance.

- `docs/rfc/0001-*` — `rfc-api` backend scope. Two cooperating processes (API + sync worker) over Postgres. Commits to PR-discussion persistence (departs from Oxide's model), OIDC/OAuth2 resource-server auth (Keycloak dev, Okta prod), Kubernetes deploy. Parent is RFC-0011 in repo-root `INGEST_RFC.md`.
- `docs/rfc/0002-*` — `rfc-site` frontend. Server-side Markdown rendering; API serves raw Markdown, never HTML.
- `docs/adr/0001-*` — Go 1.26.1 + stdlib `net/http` (Go 1.22+ `ServeMux` patterns). No HTTP framework. An earlier draft selected Echo v5 and was reversed before any code — rationale in the ADR's #Alternatives.
- `docs/adr/0002-*` — PostgreSQL as datastore.
- `docs/adr/0003-*` — Meilisearch for search, behind the API (not direct-from-frontend).
- `docs/design/0001-*` — HTTP server structure. Module layout, middleware chain (outermost `otelhttp` → recover → request-id → slog logger → timeout → CORS → rate-limit → auth), RFC 7807 error envelope, single-binary with `serve`/`work` sub-commands.
- `docs/design/0002-*` — `DocumentType` extensibility. **Load-bearing rule:** *type is a parameter, not a package name.* No `internal/rfc/`, no `GetRFC()`, no `rfc_*` columns. Handlers, services, storage, and search take `DocumentType` (or the canonical display id) as input. URL shape is `/api/v1/{type}/{id}` mounted from a registry loop at startup, plus cross-type `/api/v1/docs` and `/api/v1/search`. `{id}` is numeric in URLs; canonical display id is `RFC-0001` (prefix + dash + zero-padded number).
- `docs/investigation/0001-*` — Oxide RFD architecture case study informing the above.

Before proposing architectural changes or writing code, check the relevant doc's #Open Questions and #Resolved Decisions to see what's still in play vs. firm. When a decision changes, update the doc in the same change as the code — these docs are meant to evolve with the implementation, not freeze once accepted.

## CI / release architecture

- `.github/workflows/ci.yml` — lint, test (with Codecov), govulncheck + Trivy, goreleaser `build --snapshot`, and **docker bake** (`docker-bake.hcl`, target `ci`) with GHA cache.
- `docker-bake.hcl` — `ci` target cross-builds `linux/amd64` + `linux/arm64`; `local` target single-arch dev image. `VERSION` / `COMMIT` / `REGISTRY` / `IMAGE_NAME` / `IMAGE_TAG` variables; `make release-local` smoke uses `docker buildx bake local --load`.
- `Dockerfile` — multi-stage `golang:1.26.1-alpine` builder → `gcr.io/distroless/static:nonroot` runtime. Build cache mounts, `-trimpath -ldflags -s -w` with `main.version` / `main.commit` injected from bake vars. Never built via compose.
- `.github/workflows/release.yml` — tag-triggered goreleaser release.
- `.github/workflows/pr-labels.yml` + `.github/labeler.yml` — branch-prefix auto-labeling (`feat/`, `fix/`, `chore/`, `docs/`, `bug/`). When creating branches, use those prefixes so the label automation works.
- `scripts/labels.sh` — one-time GitHub label bootstrap.

## Conventions to preserve

- **Uber Go style** via `.golangci.yml` (errcheck, errorlint, gocyclo, gocognit, funlen, prealloc, etc.) — see the file for the full enabled list before adding code that might trip it.
- `goimports -local github.com/donaldgifford` grouping is enforced by `make fmt`.
- Version info is injected via ldflags at build — wire any new `main` packages the same way.
- **`net/http` import is confined to `cmd/rfc-api/` and `internal/server/`.** Domain, service, store, and search packages return framework-agnostic types; the HTTP seam translates them.
- **Route metadata flows through `internal/server/routectx`**, not `r.Pattern`. Any middleware that needs the matched pattern after dispatch installs a `*routectx.Capture` (see `middleware/metrics.go`) rather than reading `r.Pattern`.
- **`os.Getenv` only in `internal/config/`** — enforced by a test (`internal/config/lint_test.go`). New config surfaces go through `Config` + `loadEnv`.
- **Type is a parameter, not a package name.** No `internal/rfc/`, no `Docs.GetRFC()`, no `rfc_*` columns. Handlers take `DocumentType` / canonical id via `routectx` + `docid.Canonical`.
- **List endpoints return bare JSON arrays** with pagination in headers (`X-Total-Count`, RFC 8288 `Link`). Never `null` — `render.ArrayJSON` normalizes nil slices to `[]`.
- **Errors flow through domain sentinels → `httperr.Write`.** Handlers never encode errors directly; every 4xx/5xx is `application/problem+json` (RFC 7807).
- **Any change to `api/openapi.yaml` must keep `test/contract/` green.** The spec and handlers are validated against each other in-process on every CI run.
- **A new `domain.Err*` sentinel requires a matching case in `httperr.classify`.** Otherwise it silently falls through to the 500 default and the client detail is replaced with the fixed "an internal error occurred" string. This seam is how the rate-limit 429-vs-500 bug shipped: the error was passed to `httperr.Write` but no classifier case matched, so the response was 500 / problem+json even though `Retry-After` was set.
- **HTTP status assertions in tests are exact, not `!= 200`.** A permissive check like `rec.Code != 200` passed for a request that should have returned 429 but was actually returning 500. Assert the specific expected status (and, where it matters, `Content-Type: application/problem+json` for error paths).
- **Never use `§` (section symbol) in this repo.** Use plain `#` for section references in comments, markdown, configs, commit messages, and PR bodies. The 28-file cleanup that introduced this rule is in git history — don't reintroduce it.

## Pitfalls the tooling is opinionated about

- **OTel semconv version** must match the SDK default schema URL. On this tree, `sdk.Default()` returns `v1.40.0`; importing `semconv/v1.26.0` and merging into that resource fails with a "conflicting Schema URL" at runtime. Keep the import at `semconv/v1.40.0`.
- **`gocritic hugeParam` fires at 80 bytes.** Functions that take a struct of that size or larger by value get flagged — take `*T` instead (e.g. `middleware.CORS(*CORSConfig)`, `server.New(*Deps)`).
- **Every `//nolint` directive needs an inline justification comment.** `nolintlint` fails otherwise. Pattern: `//nolint:contextcheck // background shutdownCtx intentional; caller ctx is canceled`. Avoid carrying `//nolint:wrapcheck` — `wrapcheck` is not enabled in `.golangci.yml` and the directive would itself be dead code.
- **`httptest.NewRequest` trips `noctx`.** Use `httptest.NewRequestWithContext(t.Context(), method, url, http.NoBody)` for test requests, and `http.NoBody` (not `nil`) for the body when there isn't one (`gocritic httpNoBody`).
- **`kin-openapi` is strict about OAS 3.1 features.** `info.summary` is rejected ("extra sibling fields"), and `const: value` in a schema must be written as `enum: [value]`. When adding to `api/openapi.yaml`, run `go test ./test/contract/...` immediately to catch this.
- **`goreleaser --snapshot` output goes to `dist/`.** That directory is gitignored — don't `git add -A` without checking.
- **`govulncheck` must be built with the same Go version as the source tree.** Version skew reports "Loading packages failed" and exits 0. If it's reporting nothing useful, `go install golang.org/x/vuln/cmd/govulncheck@latest` with the current toolchain and retry.
