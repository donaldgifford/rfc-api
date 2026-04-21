# Development

Everything you need to build, run, and test `rfc-api` on a developer
machine. For the detailed runbook (port map, compose profiles, pprof
workflow, troubleshooting) see [`local-dev.md`](./local-dev.md).

## Contents

- [`local-dev.md`](./local-dev.md) — runbook: getting started, port
  map, compose profiles, observability, pprof, troubleshooting.

## Requirements

- **[mise](https://mise.jdx.dev/)** — manages every pinned tool below.
  `mise install` at the repo root materializes the full toolchain.
- **Docker + Docker Compose** — dev dependencies (Postgres, Meilisearch,
  and optional Keycloak / OTel / Grafana / Loki stacks) run via
  `docker compose`.

Everything else (Go, golangci-lint, goimports, goreleaser, migrate
CLI, docz, etc.) is pinned in [`mise.toml`](../../mise.toml) and
installed by `mise install` — you do not need to install Go by hand.

### Pinned versions that matter

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.26.1 | language |
| golangci-lint | 2.11.4 | lint (CI pins the same) |
| goimports | latest | import grouping (`-local github.com/donaldgifford`) |
| docz | latest | structured docs (RFC/ADR/DESIGN/IMPL/PLAN/INV) |
| migrate | v4.18.1 | ad-hoc DB migration inspection |
| goreleaser | via workflow | release binaries + docker bake |

## One-command setup

```sh
mise install              # pin Go / golangci-lint / goimports / ...
cp .env.example .env      # local config; gitignored, edit freely
make compose-up           # starts Postgres + Meilisearch
go run ./cmd/rfc-api serve
```

Then hit `http://localhost:8080/api/v1/types` to confirm the server
is up. `http://127.0.0.1:8081/readyz` shows dependency status
(Postgres + Meilisearch probes).

## Common workflows

- `make test` — `go test -v -race ./...`
- `make test-integration` — Postgres + HTTP integration suite
  (requires `DATABASE_URL`)
- `make test-integration-search` — Meilisearch integration suite
  (requires `MEILI_URL` + `MEILI_MASTER_KEY`)
- `make lint` — golangci-lint (same config CI uses)
- `make fmt` — `gofmt -s -w .` + `goimports -local …`
- `make check` — pre-commit: lint + test
- `make ci` — full CI locally: lint + test + build + license-check
- `make reindex` — enqueue a reindex job per document (requires a
  running `rfc-api work` to drain them)
- `make pprof-cpu` / `pprof-heap` / `pprof-goroutine` — capture
  profiles against the admin port

Full list: `make help`.

## Architecture docs

Implementation choices are recorded in ADRs, designed in DESIGN docs,
and rolled out via IMPL plans. Before writing code, skim the relevant
doc's **Open Questions** and **Resolved Decisions** sections — they
show what's firm vs. still in play.

- [`docs/rfc/`](../rfc/) — scope + user-facing design.
- [`docs/adr/`](../adr/) — architecture decisions (Go + stdlib,
  Postgres, Meilisearch).
- [`docs/design/`](../design/) — server structure, DocumentType
  extensibility model.
- [`docs/impl/`](../impl/) — shipped implementation plans with phase
  checklists. IMPL-0001..0005 are Completed.
- [`docs/investigation/`](../investigation/) — case studies informing
  the above (e.g. Oxide's RFD system).

`docz list` shows the current set of docs at a glance. Prefer
`docz create` over hand-authoring so the indexes + ids stay
consistent; `.docz.yaml` holds the type-level config
(statuses, id prefixes, etc.).

## Conventions worth knowing

- **Uber Go style** via [`.golangci.yml`](../../.golangci.yml). Read
  the file before adding code that might trip it (gocyclo, funlen,
  prealloc, errcheck, hugeParam at 80 bytes, etc.).
- **`goimports -local github.com/donaldgifford`** grouping is
  enforced by `make fmt`.
- **`net/http` is confined to `cmd/rfc-api/` and
  `internal/server/`.** Domain, service, store, and search packages
  return framework-agnostic types; the HTTP seam translates them.
- **`os.Getenv` only in `internal/config/`** — enforced by a test.
- **Type is a parameter, not a package name.** No `internal/rfc/`,
  no `Docs.GetRFC()`, no `rfc_*` columns. See
  [`docs/design/0002-*`](../design/0002-documenttype-extensibility-for-multiple-content-types.md).
- **List endpoints return bare JSON arrays** with pagination in
  headers (`X-Total-Count`, RFC 8288 `Link`).
- **Errors flow through `domain.Err*` sentinels → `httperr.Write`.**
  Every 4xx/5xx response is `application/problem+json` (RFC 7807).
- **Branch prefixes drive PR labels.** Use `feat/`, `fix/`, `chore/`,
  `docs/`, or `bug/` so `.github/labeler.yml` picks up the PR
  correctly.
- **Never use `§` (section symbol).** Plain `#` for section
  references in comments, markdown, configs, commit messages.

## Getting help

- `make help` — lists every Makefile target with a description.
- `docz list` — inventory of architecture + design docs.
- Build-ship-break questions: read the relevant IMPL first; most
  have a **Pitfalls** or **Open Questions** section with the
  gotchas.
