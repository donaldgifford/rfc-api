# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project state

[IMPL-0001][impl-0001] is **Completed** (2026-04-20). DESIGN-0001 and DESIGN-0002 are **Implemented**; ADR-0001 is **Accepted**. The HTTP server boots end-to-end:

- `cmd/rfc-api/` — `serve` + `work` subcommands under a small dispatcher; `version` / `help`; signal-rooted ctx; errgroup lifecycle for both servers.
- `internal/server/` — main + admin servers, registry-driven `/api/v1/{type}/*` router with cross-type `/docs` / `/search` / `/types`, full middleware chain (OTel → recover → request-id → logger → metrics on root; timeout → CORS → rate-limit → auth-stub on v1), RFC 7807 problem+json error envelope, per-route GitHub webhook with HMAC verification.
- `internal/domain/` — framework-agnostic `Document`, `DocumentType`, registry (prefix + id uniqueness enforced at load), `docid` pure helpers.
- `internal/service/` — service layer. `internal/search` shipped a `NoopClient` pre-IMPL-0005; the live Meili client now replaces it, with `NoopClient` kept as a test-only seam.
- `internal/obs/` — OTel TracerProvider (OTLP/gRPC when `OTEL_EXPORTER_OTLP_ENDPOINT` is set, no-op otherwise), Prometheus registry + HTTP collectors.
- `api/openapi.yaml` — hand-authored OAS 3.1. `test/contract/` validates every handler against it via `kin-openapi` on every CI run.

[IMPL-0002][impl-0002] is **Completed** (2026-04-19). The production store is Postgres end-to-end:

- `db/migrations/` — forward-only SQL; `db/embed.go` bundles them into the binary. `db.NewMigrator(dsn)` is shared by `rfc-api migrate` and the integration-test `TestMain`.
- `cmd/rfc-api/migrate.go` — `rfc-api migrate` subcommand (golang-migrate + `iofs`).
- `internal/store/postgres/` — pgx/v5 pool (`pool.go`), `store.Docs` implementation (`docs.go`) with keyset pagination on `(created_at DESC, id ASC)`, and `Probe{Pool}` for `/readyz` (`probe.go`). Wired in `cmd/rfc-api/serve.go`.
- `internal/store/memory/` survives as a **test-only** `store.Docs` fake for unit suites (server, handler, service, contract, router). Production never imports it.
- Integration tests: store-level at `internal/store/postgres/*_test.go`, server-level at `test/integration/postgres/`. Both gated `//go:build integration` and driven by `DATABASE_URL`. `make test-integration` runs them; CI `integration` job exercises on every push via a postgres:18-alpine service.

[IMPL-0003][impl-0003] is **Completed** (2026-04-20). The sync worker runs end-to-end:

- `cmd/rfc-api/work.go` — real worker lifecycle (not a stub). Opens the pgxpool, builds the document-type registry, constructs `worker.New`, runs scanner + processor sub-loops via errgroup, exposes its own admin port (`/healthz` `/readyz` `/metrics`).
- `internal/config/config.go` — `Worker` + `SourceRepo` structs; env prefix `RFC_API_WORKER_*` plus `GITHUB_TOKEN` upstream-named.
- `internal/worker/worker.go` — source validation, probes (`poolProbe`, `scanProbe`), and an errgroup lifecycle that runs the scanner + processor + admin under a single cancellation.
- Smoke targets refactored: `make smoke` (`smoke-serve` + `smoke-work` + `smoke-soak`) now ride the compose Postgres via `SMOKE_DATABASE_URL` (default `postgres://rfcapi:rfcapi@127.0.0.1:5432/rfcapi`). The old bogus-DSN pattern broke post-IMPL-0002 Phase 2 (pool pings on open).
- `internal/worker/githubsource/` — GitHub access seam (`Client`) built on `go-github/v67` + `ghinstallation/v2`. Supports App-based auth (prod) and a PAT fallback (dev); rate-limit retry with bounded backoff (`withRetry`); `ListFiles`/`GetFile`/`ListPullRequestsForFile`/`ListPullRequestComments`/`ListPullRequestFiles`. Unit-tested via httptest against a mux at `/api/v3/*`.
- `internal/worker/queue/` — Postgres-backed job queue. `Queue` has `Enqueue/Lease/Succeed/Fail/Depth`; `Lease` uses a CTE + `FOR UPDATE SKIP LOCKED` so N workers coordinate without an external broker. `Leaser` owns the poll loop + per-kind concurrency semaphore + panic recovery. Five Prometheus metrics (`rfc_api_worker_jobs_*` + `queue_depth`) live on `obs.Metrics` and render on the worker's `/metrics`. Integration tests gated `//go:build integration`.
- `internal/worker/scanner/` + `internal/worker/ingest/` — ingest pipeline. Scanner ticks every `ScannerInterval`, lists files per `SourceRepo`, diffs against `documents.source_commit`, enqueues `ingest` jobs (dedup `content:<sha>`), and hard-deletes anything the remote dropped. Ingest handler fetches, resolves the parser, parses, transactionally upserts (`postgres.Docs.Upsert` replaces authors + links, preserves `created_at`), and emits `reindex` + `discussion_fetch` jobs.
- `internal/server/handler/webhook.go` — webhook-driven reconcile. The HMAC-verified GitHub handler dispatches on `X-GitHub-Event`: `push` parses the payload and enqueues one `ingest` per touched `.md` (dedup `content:<head_commit.sha>`); `pull_request` / `pull_request_review` / `pull_request_review_comment` extract `(repo, pr_number)` and enqueue a PR-scope `discussion_fetch` job (dedup `discussion-pr:<repo>:<pr>`). API and worker share the `jobs` table through a single Postgres; no in-process queue between processes.
- `internal/worker/discussion/` — `discussion_fetch` handler. Direct mode writes one doc's `(url, comment_count, last_activity, participants)` to `discussions` + `discussion_participants` (force-push-safe: participants are delete+reinsert in the same tx). PR-scope mode lists the PR's files, matches against `SourceRepo.Path`, and fans out per-doc direct jobs. Handler self-requeues at `Active` cadence (1h) for open PRs and `Archived` (24h) for merged/closed — combined with the webhook + ingest paths, a discussion refreshes on every meaningful event without the scanner owning the quota cost.

[IMPL-0004][impl-0004] is **Completed** (2026-04-20). The parser seam ships end-to-end:

- `internal/domain/parser.go` — `Parser` interface; handlers receive `(raw []byte, DocumentType, Source)` and emit a framework-agnostic `Document`.
- `internal/parser/` — `Registry` with `Register/Get/Names`; `Default` is the process-wide registry so parser packages register at `init()`.
- `internal/parser/doczmarkdown/` — real parser for docz Markdown (YAML frontmatter + body). Two-pass YAML unmarshal isolates known fields vs. `Extensions` catch-all; canonical id, lifecycle validation, and structured authors all enforced. Link extraction via goldmark AST walk + regex fallback, dedup'd, with pre-computed `TargetURL`.
- `internal/parser/testparser/` — minimal YAML-only parser for the DESIGN-0002 fake-type harness. Registers as `test-parser`, skips Markdown + link extraction, but runs the same id-shape + prefix + lifecycle checks doczmarkdown does.
- `test/integration/faketype_test.go` — graduates DESIGN-0002's "adding a type is a config change" claim from route-mounting (in `router_test.go`) to full parse → persist → serve. Spins up a contrived `tst` type through the registry + test-parser + in-memory store + server and asserts each sub-resource endpoint returns the expected shape, plus a lifecycle-violation 400 guard.
- **Three-tier timestamp fallback.** `domain.Source` grew a `CommitTime time.Time`; `githubsource.CommitTimeForFile` resolves the upstream author date via `ListCommits(path, sha, per_page=1)`; the ingest handler threads it through. Both parsers fall back: frontmatter `created`/`updated` → `Source.CommitTime` → `time.Now()`. Re-ingests of frontmatter-less docs now carry stable archival timestamps instead of the wall clock.

[impl-0004]: ./docs/impl/0004-rfc-api-parser-plugin-seam-implementation.md

[impl-0001]: ./docs/impl/0001-rfc-api-http-server-phase-1-implementation.md
[impl-0002]: ./docs/impl/0002-rfc-api-postgresql-store-implementation.md
[impl-0003]: ./docs/impl/0003-rfc-api-sync-worker-implementation.md
[impl-0005]: ./docs/impl/0005-rfc-api-meilisearch-search-implementation.md

[IMPL-0005][impl-0005] is **Completed** (2026-04-21). Meilisearch is the canonical search surface end-to-end:

- `internal/search/meilisearch/` — SDK wrapper (`Client`), scoped constructors (`NewReadClient` / `NewWriteClient` — APIKey / WriteKey fall back to MasterKey so dev stays one-knob), readiness probe wired into `rfc-api serve`, `ApplySettings` (idempotent; searchable + filterable + sortable + ranking-with-tiebreaker), per-section `Indexer` (H1/H2 split via goldmark AST; sub-doc id `{parent}__{slug}` — Meili rejects `#`; body_excerpt capped at 500 chars with ellipsis; delete-by-filter on `parent_id` for clean re-index), `Client.Query` (offset pagination encoded as base64(`off:N`); highlight snippets + `matched_terms`; visibility-scoped), `DistinctParentsByType` for drift comparison.
- `internal/worker/reindex/` — `reindex` and `search_delete` job handlers. Reindex re-reads the authoritative Postgres row (graceful skip on ErrNotFound when ingest + tombstone race). Scanner's tombstone path now enqueues `search_delete` alongside the Postgres delete.
- `cmd/rfc-api/reindex.go` — `rfc-api reindex` enumerates every document and enqueues one job per id (dedup `doc:<id>`). `--dry-run` prints the set; `--check-drift` compares Postgres vs Meili per type and exits 1 on any non-zero delta. `make reindex` wraps it.
- `cmd/rfc-api/serve.go` — NoopClient replaced by the Meili read client; `cmd/rfc-api/work.go` builds the write-scoped Indexer and passes it to `worker.New` as `SearchIndexer`.
- CI gained a `meilisearch:v1.20` service container on the `integration` job; `make test-integration-search` exercises the live-server path (seed → query → per-type filter → delete → drift count → settings idempotency). Version matters: SDK v0.36.2 always ships `disableOnNumbers` in the typoTolerance PATCH body, so Meili < v1.12 rejects the call — keep the CI pin on v1.20 or later.
- Every Meili-task-returning call goes through `Client.awaitTask` — the SDK's bare `WaitForTask` returns a task whose `Status: "failed"` is still a "done" task, so the helper checks status + surfaces the Meili error message (invalid doc id, missing filterable attr, etc.) rather than silently succeeding.

**RFC / ADR / INV statuses are aligned with reality (2026-04-21 cleanup):** RFC-0001 Accepted, ADR-0001/0002/0003 all Accepted, INV-0001 Concluded. RFC-0002 (rfc-site frontend) stays Draft — frontend work hasn't started. The shipped-IMPL docs (0001–0005) carry the authoritative task-level history; CLAUDE.md summarizes.

**What's next:** no open IMPL plan. The natural next scope is RFC-0001 Phase 4 (auth via Keycloak dev / Okta prod + MCP-ready API polish). Author a fresh IMPL doc (`docz create impl …`) before writing code.

## Local development

See [`docs/development/`](./docs/development/) for the setup + requirements overview, or jump straight to [`docs/development/local-dev.md`](./docs/development/local-dev.md) for the runbook (port map, compose profiles, pprof workflow, troubleshooting). TL;DR: `mise install && cp .env.example .env && make compose-up && go run ./cmd/rfc-api serve`.

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
- **Integration tests that touch schema need a `TestMain` in *every* package that runs them.** `TestMain` runs per-package; one `TestMain` in `test/integration/postgres/` does not bootstrap the schema for `internal/store/postgres/*_test.go`. CI's fresh Postgres container will hit `truncate()` before the schema exists and every test fails with `relation "discussion_participants" does not exist`. Both packages need their own migrate-up hook. (Local runs pass misleadingly when the compose DB has been migrated by an earlier invocation.)
- **`govulncheck` must be built with the same Go version as the source tree.** Version skew reports "Loading packages failed" and exits 0. If it's reporting nothing useful, `go install golang.org/x/vuln/cmd/govulncheck@latest` with the current toolchain and retry.
- **Release docker job needs a `release` target in `docker-bake.hcl` + `*.output=type=registry` override.** `.github/workflows/release.yml` calls `targets: release`; without the target, bake fails immediately with `failed to find target release`. Without the `output=type=registry` override in the action's `set:` block, the build completes but nothing gets pushed (CI's `docker-build` job sets this; the release copy previously didn't). The `release` target inherits from `_common` + `docker-metadata-action` so the `docker/metadata-action`-generated bake-file tags/labels overlay correctly.
- **`docker/metadata-action`'s `images:` must be the full image reference.** `ghcr.io/donaldgifford/` (trailing slash, no image name) silently produces malformed tags like `ghcr.io/donaldgifford/:v0.0.1`. Use `ghcr.io/donaldgifford/rfc-api`.
- **`goreleaser archives.format` is deprecated.** v2.15+ wants `formats: ["tar.gz"]`. Migrate at the next release-config touch — current `.goreleaser.yml` still uses the singular key and emits a deprecation warning in every release run.
- **Meilisearch document ids accept only `[a-zA-Z0-9_-]`, max 511 bytes.** Using `#` as the parent/section separator (the natural URL-fragment shape) produces task `status: "failed"` with `invalid_document_id` — but the SDK's `WaitForTask` returns a task whose terminal state is still "done," so the caller sees no Go error. Two consequences hardwired into `internal/search/meilisearch/`: (1) sub-doc ids use `{parent}__{slug}` (double underscore), (2) every write path goes through `Client.awaitTask`, which inspects `Task.Status` and surfaces `Task.Error.Message` as a wrapped error instead of silently succeeding.
- **`GPG_PRIVATE_KEY` secret must be the secret key, not the public half.** `gpg --armor --export` gives you the public half and it imports cleanly with only `public key ... imported` in the log; goreleaser then fails signing with `gpg: No secret key`. Use `gpg --armor --export-secret-keys <fingerprint>`. The block starts with `BEGIN PGP PRIVATE KEY BLOCK`, not `PUBLIC KEY BLOCK`.
