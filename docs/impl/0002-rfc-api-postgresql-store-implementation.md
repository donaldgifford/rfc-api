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
- [Resolved Decisions](#resolved-decisions)
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

- [ ] Add `golang-migrate` to `mise.toml` (decided in
      [#Resolved Decisions](#resolved-decisions) RD1).
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

- [ ] Use `github.com/jackc/pgx/v5` native API ([RD2](#resolved-decisions));
      pure Go, no CGO.
- [ ] `internal/store/postgres/pool.go`: `NewPool(ctx, cfg)` using
      `pgxpool.New`. Honor `DATABASE_URL` (already in `config.Config`). Pool
      tuning per RD3: `MaxConns = 25`, `MinConns = 5`, `MaxConnIdleTime = 5m`,
      `HealthCheckPeriod = 30s`.
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

- [ ] `internal/store/postgres/docs.go`: implement `store.Docs`. Read
      methods are one transaction each (read-only). Methods:
  - `Get(ctx, id)` — single-row read.
  - `List(ctx, q)` — keyset pagination on `(created_at DESC, id ASC)`; honor
    `q.TypeID` filter when non-empty.
  - `Links(ctx, id)` — joined across `links`.
  - `Discussion(ctx, id)` — join with `discussions` + `discussion_participants`.
  - `Authors(ctx, id)` — ordered by `seq`.
  - `Revisions(ctx, id)` — returns empty slice in this IMPL; the revisions
    table and worker-populated data land with [IMPL-0003][impl-0003].
  - `Upsert(ctx, doc) error` — **stub** per RD7. Returns a
    well-known "not implemented in IMPL-0002" error (shape TBD by
    IMPL-0003 — likely a new `domain.ErrUnsupported` sentinel with a
    matching `httperr.classify` case mapping to 501, but IMPL-0003
    owns that). Present on the interface so worker + reindex paths
    have a stable contract to target; the real write semantics
    (transaction shape, per-row replace for `authors`/`links`) land
    in [IMPL-0003][impl-0003] Phase 4.
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

1. **Job dedup key shape.** The initial sketch used `UNIQUE (kind,
   (payload->>'content_sha'))`, which assumes every job kind carries
   `content_sha` in its payload. Not every kind will: a `reindex` or
   `discussion_fetch` keys more naturally on `document_id`, a scanner tick
   keys on `source_id`, etc. Two reasonable shapes:
   - **(a) Rename to `dedup_key text NOT NULL`** — one opaque column, each
     job kind writes its own key format (`content_sha:<sha>`,
     `doc:RFC-0001`, `source:rfc-repo/docs/rfc`). Unique on `(kind,
     dedup_key)`. Simpler constraint, pushes the choice to the producer.
   - **(b) Keep `content_sha` on ingest jobs, add a `resource_id` column
     for others**, unique on `(kind, coalesce(content_sha, resource_id))`.
     More structure, more migration if a new kind needs a different key.
   Default proposal: **(a)**. Defer the final call to IMPL-0003 where the
   concrete job kinds are designed; IMPL-0002 will ship whichever shape
   IMPL-0003 chooses.

## Resolved Decisions

1. **Migration tool: `golang-migrate`.** Ubiquitous, simple forward-only
   SQL, both CLI and library. Our migrations don't need HCL diff tooling
   (Atlas) or Go-in-migrations (goose).
2. **Driver: `github.com/jackc/pgx/v5` native API.** Pure Go, no CGO.
   Typed codecs for `jsonb`, `timestamptz`, `text[]`, and better batch
   ergonomics than the `database/sql` shim. (The `database/sql` +
   `pgx/stdlib` alternative is also pure Go — both options here are
   CGO-free; the native API wins on ergonomics.)
3. **Pool tuning defaults: `MaxConns=25`, `MinConns=5`,
   `MaxConnIdleTime=5m`, `HealthCheckPeriod=30s`.** Review once real
   traffic or a multi-replica deploy happens.
4. **Extensions storage: `jsonb` column with a GIN index.** Simpler than
   per-type side tables and consistent with DESIGN-0002's load-bearing
   "type is a parameter" rule (side tables would make adding a type a
   schema change). Revisit only if a specific per-type query pattern is
   slow.
5. **Labels storage: `text[]` column with a GIN index.** Idiomatic
   Postgres and matches the query shapes we need (`labels && ARRAY['foo']`,
   distinct label enumeration). Join table if we ever want per-label
   metadata.
6. **Migration execution: explicit subcommand + Helm Job.** `rfc-api
   migrate` is the only path that applies migrations. In prod, a Helm
   pre-install / pre-upgrade Job wraps the subcommand. Locally, `make
   migrate` runs the same path. Deliberately avoiding (a) startup-
   embedded migrations — serving readiness shouldn't be coupled to
   schema-migration success, which is a different failure mode with
   different rollback semantics.
7. **Worker writes: stub `Upsert(ctx, doc) error` on `store.Docs`.**
   Returns `domain.ErrUnsupported` in IMPL-0002 but is present on the
   interface. This gives IMPL-0003 and IMPL-0005 a stable contract to
   target, without IMPL-0002 shipping transactional write semantics that
   will be reshaped by the real worker design.

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
