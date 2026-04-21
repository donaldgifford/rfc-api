# rfc-api

Backend HTTP API for a Markdown document portal. `rfc-api` syncs RFCs,
ADRs, and any other configurable document type from GitHub content
repos, persists them to PostgreSQL, indexes them in Meilisearch for
full-text search, and serves them over a read-only JSON API.

[![CI](https://github.com/donaldgifford/rfc-api/actions/workflows/ci.yml/badge.svg)](https://github.com/donaldgifford/rfc-api/actions/workflows/ci.yml)

- **One canonical surface.** Every consumer — frontend, CLI, MCP
  server — reads from `/api/v1/*`. Meilisearch is behind the API,
  never addressed directly.
- **Type is a parameter, not a package name.** Adding a new document
  type is a config change, not a code change
  ([DESIGN-0002][design-0002]).
- **Two processes, one binary.** `rfc-api serve` runs the HTTP tier;
  `rfc-api work` runs the sync + search-write loops. Both are
  wired by [IMPL-0001][impl-0001]..[IMPL-0005][impl-0005] and ship
  in the same binary.

## Quickstart

```sh
mise install              # pin Go / golangci-lint / goimports / docz / ...
cp .env.example .env      # local config; gitignored, edit freely
make compose-up           # starts Postgres + Meilisearch
rfc-api migrate           # apply DB migrations (or: make build && ./build/bin/rfc-api migrate)
go run ./cmd/rfc-api serve
```

Then:

```sh
curl http://localhost:8080/api/v1/types      # registered document types
curl http://127.0.0.1:8081/readyz            # Postgres + Meili probes
```

To run the sync worker (in a second terminal, with `GITHUB_TOKEN`
set and a `source_repos` block configured):

```sh
go run ./cmd/rfc-api work
```

For the full dev runbook — port map, compose profiles, pprof workflow,
troubleshooting — see [`docs/development/`][docs-dev] and
[`docs/development/local-dev.md`][docs-local-dev].

## API contract

The canonical API description lives at
[`api/openapi.yaml`](./api/openapi.yaml) (OpenAPI 3.1). It is the
source of truth for `rfc-site`, MCP servers, and any other client —
the Go code is tested against it in
[`test/contract/`](./test/contract/), so the spec and the server
behavior cannot drift without a CI failure.

- `GET /api/v1/types` — registered document types.
- `GET /api/v1/docs` — cross-type paginated list.
- `GET /api/v1/search?q=…` — cross-type full-text search
  ([ADR-0003][adr-0003]).
- `GET /api/v1/{type}` / `/api/v1/{type}/{id}` — per-type surface.
- `GET /api/v1/{type}/{id}/authors` / `/links` /`/discussion` —
  sub-resources.
- `POST /api/v1/webhooks/github` — HMAC-verified; dispatches `push`
  / `pull_request*` events to the sync worker via the Postgres-backed
  job queue.

See [DESIGN-0001][design-0001] and [DESIGN-0002][design-0002] for
the server structure + DocumentType extensibility model.

## Architecture

```text
          ┌────────────┐         ┌──────────────┐
          │ rfc-api    │ SELECT  │ PostgreSQL   │
client ─► │  serve     │ ◄─────► │   (source    │
          │ (HTTP API) │         │    of truth) │
          └─────┬──────┘         └──────▲───────┘
                │                       │
                │  /api/v1/search       │ UPSERT
                ▼                       │
          ┌────────────┐         ┌──────┴───────┐
          │ Meilisearch│ ◄────── │ rfc-api work │ ◄── GitHub (webhooks + scan)
          │   (index)  │         │  (sync + idx)│
          └────────────┘         └──────────────┘
```

- `cmd/rfc-api/` — CLI dispatcher: `serve`, `work`, `migrate`,
  `reindex`, `version`, `help`.
- `internal/server/` — HTTP server (main + admin ports), middleware
  chain, routing, handlers, RFC 7807 error envelope.
- `internal/service/` — use-case layer between handlers and
  storage.
- `internal/store/postgres/` — production store (pgx/v5,
  keyset-paginated).
- `internal/store/memory/` — test-only fake for unit suites.
- `internal/domain/` — framework-agnostic types + document-type
  registry.
- `internal/parser/` — plugin seam for type-specific parsers
  ([IMPL-0004][impl-0004]).
- `internal/search/meilisearch/` — per-section indexer, read
  client, settings bootstrap, drift check ([IMPL-0005][impl-0005]).
- `internal/worker/` — scanner + processor loops + GitHub client +
  Postgres-backed job queue.
- `internal/obs/` — tracing + metrics wiring.

## Commands

`make help` for the full list. Common ones:

- `make check` — lint + test (pre-commit).
- `make ci` — lint + test + build + license-check (full local CI).
- `make run-local` — `go run ./cmd/rfc-api serve`.
- `make compose-up` — start Postgres + Meilisearch (add `-auth`,
  `-obs`, `-full` for extra dependency profiles).
- `make smoke` — CLI smoke-test suite.
- `make reindex` — enqueue a reindex for every document (worker
  must be running to drain).
- `make test-integration` — Postgres + HTTP integration suite
  (requires `DATABASE_URL`).
- `make test-integration-search` — Meilisearch integration suite
  (requires `MEILI_URL` + `MEILI_MASTER_KEY`).
- `make pprof-cpu` / `pprof-heap` / `pprof-goroutine` — profiles
  against the admin port.

## Documentation

Architecture + implementation decisions live in `docs/`, managed by
[docz](https://github.com/donaldgifford/docz). Run `docz list` for
the current inventory.

- **RFCs** — [`docs/rfc/`](./docs/rfc/)
  - [RFC-0001][rfc-0001] — rfc-api scope (Accepted).
  - [RFC-0002][rfc-0002] — rfc-site frontend (Draft).
- **Architecture decisions** — [`docs/adr/`](./docs/adr/)
  - [ADR-0001][adr-0001] — Go + stdlib `net/http`.
  - [ADR-0002][adr-0002] — PostgreSQL as datastore.
  - [ADR-0003][adr-0003] — Meilisearch for search.
- **Design** — [`docs/design/`](./docs/design/)
  - [DESIGN-0001][design-0001] — HTTP server structure.
  - [DESIGN-0002][design-0002] — DocumentType extensibility.
- **Implementation plans** — [`docs/impl/`](./docs/impl/)
  - [IMPL-0001][impl-0001] — HTTP server phase 1.
  - [IMPL-0002][impl-0002] — PostgreSQL store.
  - [IMPL-0003][impl-0003] — sync worker.
  - [IMPL-0004][impl-0004] — parser plugin seam.
  - [IMPL-0005][impl-0005] — Meilisearch search.
- **Investigations** — [`docs/investigation/`](./docs/investigation/)
  - [INV-0001][inv-0001] — Oxide RFD architecture case study.
- **Development runbook** — [`docs/development/`][docs-dev].

## Contributing

Branch prefixes drive GitHub labels via `.github/labeler.yml`: use
`feat/`, `fix/`, `chore/`, `docs/`, or `bug/`. `make check` before
pushing.

[docs-dev]: ./docs/development/
[docs-local-dev]: ./docs/development/local-dev.md

[rfc-0001]: ./docs/rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
[rfc-0002]: ./docs/rfc/0002-rfc-site-web-frontend-for-the-markdown-portal.md
[adr-0001]: ./docs/adr/0001-use-go-and-stdlib-net-http-for-rfc-api.md
[adr-0002]: ./docs/adr/0002-use-postgresql-as-the-rfc-api-datastore.md
[adr-0003]: ./docs/adr/0003-use-meilisearch-for-rfc-api-search.md
[design-0001]: ./docs/design/0001-rfc-api-http-server-go-net-http-structure.md
[design-0002]: ./docs/design/0002-documenttype-extensibility-for-multiple-content-types.md
[impl-0001]: ./docs/impl/0001-rfc-api-http-server-phase-1-implementation.md
[impl-0002]: ./docs/impl/0002-rfc-api-postgresql-store-implementation.md
[impl-0003]: ./docs/impl/0003-rfc-api-sync-worker-implementation.md
[impl-0004]: ./docs/impl/0004-rfc-api-parser-plugin-seam-implementation.md
[impl-0005]: ./docs/impl/0005-rfc-api-meilisearch-search-implementation.md
[inv-0001]: ./docs/investigation/0001-oxide-rfd-system-architecture-case-study.md
