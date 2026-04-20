# rfc-api

Backend HTTP API for a Markdown document portal. Serves RFCs, ADRs,
and other configurable content types from a GitHub content repo.

## Getting started

```sh
mise install
cp .env.example .env
make compose-up
go run ./cmd/rfc-api serve
```

See [`docs/local-dev.md`](./docs/local-dev.md) for the full runbook
(getting started, port map, compose profiles, pprof workflow,
troubleshooting).

## API contract

The canonical API description lives at
[`api/openapi.yaml`](./api/openapi.yaml) (OpenAPI 3.1). It is the
source of truth for `rfc-site`, MCP servers, and any other client —
the Go code is tested against it in
[`test/contract/`](./test/contract/), so the spec and the server
behavior cannot drift without a CI failure.

- `GET /api/v1/types` — registered document types.
- `GET /api/v1/docs` — cross-type paginated list.
- `GET /api/v1/search` — cross-type search.
- `GET /api/v1/{type}` / `/api/v1/{type}/{id}` — per-type surface.

See [DESIGN-0001](./docs/design/0001-rfc-api-http-server-go-net-http-structure.md)
and [DESIGN-0002](./docs/design/0002-documenttype-extensibility-for-multiple-content-types.md)
for the rationale.

## Architecture

- `cmd/rfc-api/` — CLI dispatcher (`serve`, `work`).
- `internal/server/` — HTTP server, middleware, handlers, routing.
- `internal/service/` — use-case layer between handlers and
  storage.
- `internal/store/` — storage interfaces. `memory/` is Phase 2;
  Postgres lands in a later phase.
- `internal/domain/` — framework-agnostic business types and the
  document-type registry.
- `internal/obs/` — tracing + metrics wiring.
- `api/openapi.yaml` — hand-authored contract.
- `docs/` — RFCs, ADRs, design docs, IMPL plans.

## Commands

`make help` for the full list. The most commonly used:

- `make check` — lint + test.
- `make ci` — lint + test + build + license-check.
- `make run-local` — `go run ./cmd/rfc-api serve`.
- `make compose-up` — start the local dev dependencies.
- `make smoke` — run the smoke-test suite.
