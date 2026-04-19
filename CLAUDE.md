# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project state

Early-stage skeleton. `cmd/rfc-api/main.go` is an empty `package main` stub and there is **no `go.mod` yet**. CI (`.github/workflows/ci.yml`) calls `actions/setup-go` with `go-version-file: go.mod`, so any Go work likely begins with `go mod init github.com/donaldgifford/rfc-api`. Module path is `github.com/donaldgifford/rfc-api` (see `GO_PACKAGE` in `Makefile`).

Despite the empty source tree, the repo is heavily pre-wired with tooling, CI, lint, release, and documentation infrastructure — edits here are usually to that tooling, not application code.

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

Note: `make run` currently points to `./build/bin/repo-guardian` (stale — likely a copy-paste bug to fix if you run it). `make run-local` uses the correct `$(BIN_DIR)/$(PROJECT_NAME)` path.

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
- `docs/adr/0001-*` — Go 1.26.1 + stdlib `net/http` (Go 1.22+ `ServeMux` patterns). No HTTP framework. An earlier draft selected Echo v5 and was reversed before any code — rationale in the ADR's §Alternatives.
- `docs/adr/0002-*` — PostgreSQL as datastore.
- `docs/adr/0003-*` — Meilisearch for search, behind the API (not direct-from-frontend).
- `docs/design/0001-*` — HTTP server structure. Module layout, middleware chain (outermost `otelhttp` → recover → request-id → slog logger → timeout → CORS → rate-limit → auth), RFC 7807 error envelope, single-binary with `serve`/`work` sub-commands.
- `docs/design/0002-*` — `DocumentType` extensibility. **Load-bearing rule:** *type is a parameter, not a package name.* No `internal/rfc/`, no `GetRFC()`, no `rfc_*` columns. Handlers, services, storage, and search take `DocumentType` (or the canonical display id) as input. URL shape is `/api/v1/{type}/{id}` mounted from a registry loop at startup, plus cross-type `/api/v1/docs` and `/api/v1/search`. `{id}` is numeric in URLs; canonical display id is `RFC-0001` (prefix + dash + zero-padded number).
- `docs/investigation/0001-*` — Oxide RFD architecture case study informing the above.

Before proposing architectural changes or writing code, check the relevant doc's §Open Questions and §Resolved Decisions to see what's still in play vs. firm. When a decision changes, update the doc in the same change as the code — these docs are meant to evolve with the implementation, not freeze once accepted.

## CI / release architecture

- `.github/workflows/ci.yml` — lint, test (with Codecov), govulncheck + Trivy, goreleaser `build --snapshot`, and **docker bake** (`docker-bake.hcl`, target `ci`) with GHA cache. Note: `docker-bake.hcl` is referenced but not present in the repo root — it will need to be added before the `docker-build` job will pass.
- `.github/workflows/release.yml` — tag-triggered goreleaser release.
- `.github/workflows/pr-labels.yml` + `.github/labeler.yml` — branch-prefix auto-labeling (`feat/`, `fix/`, `chore/`, `docs/`, `bug/`). When creating branches, use those prefixes so the label automation works.
- `scripts/labels.sh` — one-time GitHub label bootstrap.

## Conventions to preserve

- **Uber Go style** via `.golangci.yml` (errcheck, errorlint, gocyclo, gocognit, funlen, prealloc, etc.) — see the file for the full enabled list before adding code that might trip it.
- `goimports -local github.com/donaldgifford` grouping is enforced by `make fmt`.
- Version info is injected via ldflags at build — wire any new `main` packages the same way.
