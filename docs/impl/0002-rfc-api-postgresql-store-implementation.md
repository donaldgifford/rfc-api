---
id: IMPL-0002
title: "rfc-api PostgreSQL store implementation"
status: Draft
author: Donald Gifford
created: 2026-04-20
---
<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0002: rfc-api PostgreSQL store implementation

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-04-20

<!--toc:start-->
- [Objective](#objective)
- [Scope](#scope)
  - [In Scope](#in-scope)
  - [Out of Scope](#out-of-scope)
- [Implementation Phases](#implementation-phases)
  - [Phase 1: Schema and migrations](#phase-1-schema-and-migrations)
    - [Tasks](#tasks)
    - [Success Criteria](#success-criteria)
  - [Phase 2: Driver, pool, and config](#phase-2-driver-pool-and-config)
    - [Tasks](#tasks-1)
    - [Success Criteria](#success-criteria-1)
  - [Phase 3: store.Docs Postgres implementation](#phase-3-storedocs-postgres-implementation)
    - [Tasks](#tasks-2)
    - [Success Criteria](#success-criteria-2)
  - [Phase 4: Readiness probe and integration tests](#phase-4-readiness-probe-and-integration-tests)
    - [Tasks](#tasks-3)
    - [Success Criteria](#success-criteria-3)
  - [Phase 5: Swap-in and remove the in-memory store](#phase-5-swap-in-and-remove-the-in-memory-store)
    - [Tasks](#tasks-4)
    - [Success Criteria](#success-criteria-4)
- [File Changes](#file-changes)
- [Testing Plan](#testing-plan)
- [Dependencies](#dependencies)
- [Open Questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Objective

Replace the in-memory `store.Docs` implementation with a PostgreSQL-backed one
per [ADR-0002][adr-0002]. End state: `rfc-api serve` reads documents from
Postgres; the `store.Docs` interface is unchanged, so nothing above the store
seam moves. This unblocks the sync worker ([IMPL-0003][impl-0003]) which needs
durable storage to write into.

**Implements:** [ADR-0002][adr-0002] (storage leg of [RFC-0001][rfc-0001]
Phase 1).

## Scope

### In Scope

- Schema + forward-only migrations for `documents`, `links`, `authors`,
  `discussions`, and the `jobs` table used by the worker in
  [IMPL-0003][impl-0003].
- Driver + connection-pool wiring (`internal/store/postgres/`).
- Concrete `store.Docs` implementation using keyset pagination on
  `(created_at DESC, id ASC)` per [DESIGN-0001][design-0001] #API surface.
- Real `ReadinessProbe` that pings Postgres, replacing the Phase-2 placeholder
  at `internal/store/memory/postgres_probe.go`.
- `rfc-api migrate` subcommand — explicit migration entrypoint so cluster
  deploys can run it as a Job, and local dev can run it via `make migrate`.
- Integration tests against a real Postgres via `testcontainers-go`.
- Swap-in: `cmd/rfc-api/serve.go` wires the Postgres store; `internal/store/
  memory/` is deleted.

### Out of Scope

- **Worker writes.** The `jobs` table schema is defined here (the worker
  consumes it), but job production, leasing, and retry logic belong to
  [IMPL-0003][impl-0003].
- **Search indexing.** Meilisearch integration is [IMPL-0005][impl-0005]; the
  store exposes enough to enumerate documents for reindex, but the ingestion
  path is worker-owned.
- **Postgres full-text search.** ADR-0002 explicitly carves this out; search
  lives in Meilisearch. `pg_trgm` stays available as a fallback if we ever
  need it, but not wired in v1.
- **Multi-tenancy / RLS.** v1 is single-tenant.
- **Backup / DR policy.** ADR-0002 leans on the "store is rebuildable from
  Git" property; formal backup policy is an ops concern, not an IMPL.

## Implementation Phases

Each phase builds on the previous one. A phase is complete when all its tasks
are checked off and its success criteria are met.

---

### Phase 1: Schema and migrations

Define the relational shape for documents and lock in a migration tool + file
layout. This is the phase with the most review surface — once the schema
ships, changes become migrations rather than rewrites.

#### Tasks

- [ ] Pick a migration tool (see [#Open Questions](#open-questions) Q1) and
      add it to `mise.toml`.
- [ ] Create `db/migrations/` with forward-only `.sql` files numbered
      `0001_init.sql`, `0002_*.sql`, …
- [ ] Schema v1:
  - `documents` — primary table. Columns: `id text PRIMARY KEY` (canonical
    display id, `RFC-0001`), `type text NOT NULL` (FK-like to registry),
    `title text NOT NULL`, `status text`, `body text`, `created_at
    timestamptz NOT NULL`, `updated_at timestamptz NOT NULL`, `labels text[]`,
    `extensions jsonb`, `source_repo text`, `source_path text`,
    `source_commit text`. Index on `(type, created_at DESC, id ASC)` and
    `(created_at DESC, id ASC)` to serve `/api/v1/{type}` and `/api/v1/docs`
    without sorts.
  - `authors` — one row per (document_id, author). Columns: `document_id text
    REFERENCES documents(id) ON DELETE CASCADE`, `name text NOT NULL`, `email
    text`, `handle text`, plus a positional `seq int` so author order is
    stable on read.
  - `links` — one row per edge. Columns: `source_id text REFERENCES
    documents(id) ON DELETE CASCADE`, `target_id text NOT NULL`, `direction
    text NOT NULL CHECK (direction IN ('incoming','outgoing'))`, `label text`.
    Indexes on `source_id` and `target_id`.
  - `discussions` — document-id-keyed summary. Columns: `document_id text
    PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE`, `url text`,
    `comment_count int NOT NULL DEFAULT 0`, `last_activity timestamptz`.
    Participants live in a separate `discussion_participants` join table.
  - `jobs` — the worker's queue (shape set here, semantics in
    [IMPL-0003][impl-0003]). Columns: `id uuid PRIMARY KEY`, `kind text
    NOT NULL`, `payload jsonb NOT NULL`, `state text NOT NULL DEFAULT
    'queued'`, `attempts int NOT NULL DEFAULT 0`, `locked_by text`, `locked_at
    timestamptz`, `run_after timestamptz NOT NULL DEFAULT now()`, `created_at
    timestamptz NOT NULL DEFAULT now()`, `updated_at timestamptz NOT NULL
    DEFAULT now()`, UNIQUE constraint on the idempotency key
    `(kind, (payload->>'content_sha'))` per RFC-0001 #Sync.
- [ ] `rfc-api migrate` subcommand: reads `DATABASE_URL`, runs migrations,
      exits. Codes: 0 ok, 1 failure.
- [ ] `make migrate` target (calls the subcommand).
- [ ] `make ci` includes a smoke that spins Postgres up via compose (already
      wired), runs migrate, drops the DB — catches schema-only regressions.

#### Success Criteria

- `docker compose up postgres` + `make migrate` produces every table above
  with the indexes and constraints documented.
- Migrations are idempotent: running `make migrate` twice is a no-op on the
  second run.
- `\d documents`, `\d jobs`, etc. in `psql` show the expected schema.
- Rolling a new migration (empty one, just for the test) appends a file and
  re-run migrates cleanly.

---

### Phase 2: Driver, pool, and config

Wire the Go ↔ Postgres seam. This phase ships no new features; it makes the
service connection-aware.

#### Tasks

- [ ] Pick a driver (see [#Open Questions](#open-questions) Q2). Default
      assumption: `github.com/jackc/pgx/v5` native API (not the `database/sql`
      shim) so domain types map cleanly to Postgres types (`pgtype.Timestamptz`
      etc.).
- [ ] `internal/store/postgres/pool.go`: `NewPool(ctx, cfg)` using
      `pgxpool.New`. Honor `DATABASE_URL` (already in `config.Config`). Pool
      tuning defaults: `MaxConns = 25`, `MinConns = 5`, `MaxConnIdleTime =
      5m`, `HealthCheckPeriod = 30s` — reviewable via
      [#Open Questions](#open-questions) Q3.
- [ ] `cmd/rfc-api/serve.go`: open the pool after config load, before server
      construction; `pool.Close()` in the defer chain under the shutdown
      context.
- [ ] Log the driver version, pool settings, and server version on first
      successful connection (operator-visible, one line, INFO).
- [ ] Pool exposes a `Ping(ctx)` for the readiness probe (see Phase 4).

#### Success Criteria

- `rfc-api serve` with `DATABASE_URL=postgres://...` opens a pool at startup
  and closes it on shutdown without leaking connections.
- `SELECT 1` round-trips via the pool in a smoke test.
- Killing Postgres mid-flight produces a `ReadinessProbe` failure on the
  next `/readyz` hit (see Phase 4) rather than a panic.

---

### Phase 3: store.Docs Postgres implementation

Port every `store.Docs` method to SQL. The in-memory store stays alive during
this phase so tests that don't need Postgres keep passing.

#### Tasks

- [ ] `internal/store/postgres/docs.go`: implement `store.Docs`. Each method
      is one transaction (read-only). Methods:
  - `Get(ctx, id)` — single-row read.
  - `List(ctx, q)` — keyset pagination on `(created_at DESC, id ASC)`; honor
    `q.TypeID` filter when non-empty.
  - `Links(ctx, id)` — joined across `links`.
  - `Discussion(ctx, id)` — join with `discussions` + `discussion_participants`.
  - `Authors(ctx, id)` — ordered by `seq`.
  - `Revisions(ctx, id)` — returns empty slice in this IMPL; the revisions
    table and worker-populated data land with [IMPL-0003][impl-0003].
- [ ] Cursor encode/decode: reuse `internal/server/cursor` (already exists);
      the store takes a `*store.Cursor`.
- [ ] Nil-slice normalization: empty result sets return `[]domain.Link{}`,
      not `nil`, so `render.ArrayJSON` doesn't have to special-case.
- [ ] All SQL is parameterized. No `fmt.Sprintf` into query strings.
- [ ] Error translation: `pgx.ErrNoRows` → `domain.ErrNotFound`; unique
      violations → `domain.ErrConflict`; connection/transport errors →
      `domain.ErrUpstream` (wrapped with context).

#### Success Criteria

- `go test ./internal/store/postgres/...` passes against a real Postgres
  (via testcontainers; see Phase 4).
- Every `store.Docs` method has at least one happy-path test and one
  error-branch test.
- Keyset pagination is stable under concurrent inserts: inserting a row
  mid-page does not cause the subsequent page to skip or duplicate. (Proven
  by an integration test that inserts mid-pagination.)

---

### Phase 4: Readiness probe and integration tests

Replace the placeholder probe and prove the whole stack against a real
Postgres.

#### Tasks

- [ ] `internal/store/postgres/probe.go`: `Probe{pool}` with `Name() string`
      and `Check(ctx) error`. Uses `pool.Ping(ctx)` with a short per-request
      timeout so a wedged DB doesn't stall the readiness endpoint.
- [ ] `cmd/rfc-api/serve.go`: register the real probe; delete the
      placeholder at `internal/store/memory/postgres_probe.go`.
- [ ] Integration test suite at `test/integration/postgres/`:
  - Spins Postgres via `testcontainers-go` with `postgres:18-alpine`.
  - Runs migrations.
  - Seeds JSON fixtures from `testdata/` (same fixtures the in-memory store
    used — this is the "same seed, different store" invariant).
  - Exercises each endpoint end-to-end against the real store.
- [ ] `make test` still passes without Docker running (integration suite is
      tagged `//go:build integration` and only runs on `make test-integration`
      or in CI).
- [ ] CI (`.github/workflows/ci.yml`) runs the integration suite on every
      push — a dedicated job with a Postgres service container so the
      in-process testcontainers reuse works.

#### Success Criteria

- `/readyz` returns 503 when Postgres is unreachable; 200 when it's up.
- The integration suite is green in CI on every push.
- Contract test (`test/contract/`) stays green — the swap to Postgres does
  not change any wire shape.

---

### Phase 5: Swap-in and remove the in-memory store

Flip the default, delete the fallback, update the docs.

#### Tasks

- [ ] `cmd/rfc-api/serve.go`: Postgres store is the only store wired.
- [ ] Delete `internal/store/memory/` (including the placeholder probe).
      `testdata/` moves to `test/integration/postgres/testdata/` — the
      fixtures still drive integration tests.
- [ ] `CLAUDE.md`: update "Project state" to reflect the store swap; add any
      new pitfalls uncovered during implementation.
- [ ] Update [IMPL-0001][impl-0001]: flip the "store: in-memory for
      Phase 2" note to "store: Postgres (see IMPL-0002)".

#### Success Criteria

- `grep -r "internal/store/memory" .` returns nothing outside git history.
- `make ci` + `make test-integration` green.
- A fresh clone can `mise install && cp .env.example .env && make
  compose-up && make migrate && go run ./cmd/rfc-api serve` and have a
  running Postgres-backed binary with zero manual steps.

---

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `db/migrations/0001_init.sql` | Create | Initial schema. |
| `cmd/rfc-api/migrate.go` | Create | `rfc-api migrate` subcommand. |
| `cmd/rfc-api/main.go` | Modify | Dispatch `migrate` in addition to `serve` / `work`. |
| `cmd/rfc-api/serve.go` | Modify | Open pool; register real probe. |
| `internal/store/postgres/pool.go` | Create | `pgxpool` wiring. |
| `internal/store/postgres/docs.go` | Create | `store.Docs` implementation. |
| `internal/store/postgres/probe.go` | Create | Real `ReadinessProbe`. |
| `internal/store/postgres/*_test.go` | Create | Unit + integration coverage. |
| `internal/store/memory/` | Delete | Superseded by Postgres store. |
| `test/integration/postgres/` | Create | testcontainers suite. |
| `Makefile` | Modify | `migrate`, `test-integration` targets. |
| `.github/workflows/ci.yml` | Modify | Add integration job with Postgres service. |
| `docs/impl/0001-*.md` | Modify | Note "store: Postgres via IMPL-0002". |
| `CLAUDE.md` | Modify | Project state + any new pitfalls. |

## Testing Plan

- **Unit** — each `store.Docs` method: happy path, not-found, conflict,
  upstream-error. Cursor encode/decode round-trip.
- **Integration** — full server against a real Postgres, exercised via
  testcontainers. Covers: pagination stability under concurrent insert,
  keyset correctness at page boundaries, probe transitions, migration
  idempotency.
- **Contract** — existing `test/contract/` suite stays green; the store swap
  must not change the wire shape.
- **Coverage** — ≥ 80% on `internal/store/postgres/` (consistent with
  IMPL-0001's bar).

## Dependencies

- **ADR-0002** for the decision to use Postgres 18.
- **DESIGN-0001** for keyset pagination shape and cursor format.
- **DESIGN-0002** for the `type` column invariant (`type` is a parameter,
  one column, filterable).
- **IMPL-0001** for `store.Docs` interface shape — unchanged here.
- **IMPL-0003 (worker)** depends on the `jobs` table defined in Phase 1.
- **IMPL-0005 (Meilisearch)** depends on this store as the source of truth
  for reindex.

New Go modules:

- `github.com/jackc/pgx/v5` — driver + pool (subject to OQ2).
- Migration tool per OQ1 (likely `github.com/golang-migrate/migrate/v4`
  binary, not vendored).
- `github.com/testcontainers/testcontainers-go/modules/postgres` — testing.

## Open Questions

1. **Migration tool choice.** `golang-migrate` (ubiquitous, simple
   forward-only SQL, CLI + library), `atlas` (HCL-based, richer diff
   tooling), or `goose` (Go migration functions alongside SQL)? Default:
   `golang-migrate` for its ubiquity and the fact that our migrations don't
   need anything beyond SQL.
2. **Driver choice: `pgx/v5` native vs `database/sql` + `pgx/stdlib`.**
   Native gives us typed codecs for `jsonb`, `timestamptz`, `text[]`, and
   better batch ergonomics; `database/sql` gives us drop-in replaceability
   (hypothetical). Default: native. Reversible later if it becomes a pain.
3. **Pool tuning defaults.** MaxConns=25 / MinConns=5 is a guess for a
   single API replica. Review once we have real traffic, or before any
   multi-replica deploy.
4. **Extensions storage: `jsonb` column vs side table per type.** DESIGN-0002
   leaves this open. `jsonb` is simpler and matches the open-ended-map-at-API
   shape; side tables are friendlier to indexing but commit us to "adding a
   type is a schema change" which contradicts DESIGN-0002's load-bearing rule.
   Default: `jsonb` with GIN index. Revisit only if a specific per-type query
   pattern is slow.
5. **Labels storage: `text[]` column vs join table.** Similar trade-off.
   `text[]` with a GIN index is idiomatic Postgres and the query shapes we
   need (`labels && ARRAY['foo']`, list distinct labels) work cleanly.
   Default: `text[]`. Join table if we ever want per-label metadata.
6. **Migration execution policy.** (a) Startup — `rfc-api serve` applies
   pending migrations before binding, (b) Sidecar Job — a Helm-managed
   `rfc-api-migrate` Job runs before the Deployment's pods, (c) Explicit
   only — operator runs `rfc-api migrate` out of band. Default: (c) explicit
   via the subcommand; Helm wires (b) as a pre-install/pre-upgrade Job.
   Sidestepping (a) because it couples HTTP-serving readiness to schema
   migration success, which is a different failure mode.
7. **Content-SHA uniqueness on `jobs`.** The `UNIQUE (kind,
   (payload->>'content_sha'))` constraint assumes every job carries
   `content_sha` in its payload. This matches RFC-0001 #Sync but needs
   IMPL-0003 to confirm it holds for every job kind (scanner, webhook,
   discussion-fetch, reindex-request).
8. **Worker writes during this IMPL.** IMPL-0002 ships read-only SQL because
   the worker isn't written yet. Do we stub a minimal write path (`Upsert`
   on `store.Docs`) so the contract is visible before IMPL-0003, or leave
   writes entirely to IMPL-0003? Default: leave to IMPL-0003 — premature
   otherwise, and the interface will evolve.

## References

- [ADR-0002: Use PostgreSQL as the rfc-api datastore][adr-0002]
- [DESIGN-0001: rfc-api HTTP server — Go + net/http structure][design-0001]
- [DESIGN-0002: DocumentType extensibility][design-0002]
- [IMPL-0001: rfc-api HTTP server phase 1 implementation][impl-0001]
- [IMPL-0003: rfc-api sync worker implementation][impl-0003]
- [IMPL-0004: rfc-api parser plugin seam implementation][impl-0004]
- [IMPL-0005: rfc-api Meilisearch search implementation][impl-0005]
- [RFC-0001: rfc-api][rfc-0001]

[adr-0002]: ../adr/0002-use-postgresql-as-the-rfc-api-datastore.md
[design-0001]: ../design/0001-rfc-api-http-server-go-net-http-structure.md
[design-0002]: ../design/0002-documenttype-extensibility-for-multiple-content-types.md
[impl-0001]: ./0001-rfc-api-http-server-phase-1-implementation.md
[impl-0003]: ./0003-rfc-api-sync-worker-implementation.md
[impl-0004]: ./0004-rfc-api-parser-plugin-seam-implementation.md
[impl-0005]: ./0005-rfc-api-meilisearch-search-implementation.md
[rfc-0001]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
