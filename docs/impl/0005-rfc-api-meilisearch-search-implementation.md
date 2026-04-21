---
id: IMPL-0005
title: "rfc-api Meilisearch search implementation"
status: Draft
author: Donald Gifford
created: 2026-04-20
---
<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0005: rfc-api Meilisearch search implementation

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-04-20

<!--toc:start-->
- [Objective](#objective)
- [Scope](#scope)
  - [In Scope](#in-scope)
  - [Out of Scope](#out-of-scope)
- [Implementation Phases](#implementation-phases)
  - [Phase 1: Client, config, and key separation](#phase-1-client-config-and-key-separation)
    - [Tasks](#tasks)
    - [Success Criteria](#success-criteria)
  - [Phase 2: Index schema and settings](#phase-2-index-schema-and-settings)
    - [Tasks](#tasks-1)
    - [Success Criteria](#success-criteria-1)
  - [Phase 3: Per-section indexing + worker-driven writes](#phase-3-per-section-indexing--worker-driven-writes)
    - [Tasks](#tasks-2)
    - [Success Criteria](#success-criteria-2)
  - [Phase 4: search.Client implementation + service.Search wiring](#phase-4-searchclient-implementation--servicesearch-wiring)
    - [Tasks](#tasks-3)
    - [Success Criteria](#success-criteria-3)
  - [Phase 5: Reindex command and reconciliation](#phase-5-reindex-command-and-reconciliation)
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

Replace the `search.NoopClient` with a real Meilisearch-backed implementation
per [ADR-0003][adr-0003]. The worker ([IMPL-0003][impl-0003]) writes on
ingest; the API's `GET /api/v1/search` proxies queries. Consumers never
address Meilisearch directly — the API is the single canonical surface.

**Implements:** [ADR-0003][adr-0003]; depends on [IMPL-0002][impl-0002] for
the source-of-truth store and [IMPL-0003][impl-0003] for ingest-time writes.

## Scope

### In Scope

- Meilisearch Go client wiring in `internal/search/meilisearch/`.
- Read-scoped API key held by the API, write-scoped API key held by the
  worker — per ADR-0003.
- Index settings: searchable attributes, filterable attributes, sortable
  attributes, ranking rules, typo tolerance, faceting.
- Per-section sub-document indexing per ADR-0003 #Ingest ("documents are
  split into sub-sections (heading hierarchy) and indexed per-section so
  that search hits resolve to headings").
- Extensions field flattening (namespaced by type prefix) so per-type
  filtered search works without bloating the core schema.
- `search.Client` interface implementation (replaces `NoopClient`).
- `service.Search.Query` translation from API query params to Meili query
  DSL; pagination headers; highlight rendering.
- Reindex command (`rfc-api reindex`) that enumerates Postgres and
  rebuilds the index; used after schema changes or index loss.
- Integration tests via `meilisearch/meilisearch:v1` container.

### Out of Scope

- **Visibility filtering.** Stored field ornamental in v1 (internal-
  network only); enforced when OIDC auth lands (RFC-0001 Phase 4).
- **Synonym / stopword curation.** Meili defaults are fine for launch;
  curation is a post-launch concern.
- **Facet UI.** The API can return facet counts; rendering them is
  `rfc-site` scope.
- **Meilisearch Cloud.** Self-hosted on-cluster per ADR-0003. Revisit
  only if operations cost becomes a real line item.

## Implementation Phases

Each phase builds on the previous one. A phase is complete when all its tasks
are checked off and its success criteria are met.

---

### Phase 1: Client, config, and key separation

Wire the Meili SDK into both processes with correctly scoped credentials.

#### Tasks

- [x] Pick the SDK (default: `github.com/meilisearch/meilisearch-go`,
      the official client). Pulled in at v0.36.2.
- [x] `internal/config/config.go`: add `Meili` struct with `URL string`
      (upstream-standard: `MEILI_URL`), `MasterKey string`
      (`MEILI_MASTER_KEY`, already reserved), `APIKey string`
      (`MEILI_API_KEY` — read-scoped for the API), `WriteKey string`
      (`MEILI_WRITE_KEY` — scoped for the worker). Env-var naming follows
      the memory rule (upstream name unchanged for external deps).
      `ReadKey()` / `WriteSecret()` helpers fall back to MasterKey when
      explicit keys are unset (dev single-knob pattern).
- [x] `internal/search/meilisearch/client.go`: `Client{c *meilisearch.
      Client}`. Constructors `NewReadClient(cfg)` and `NewWriteClient(cfg)`
      that pick the right key. Ping() wraps HealthWithContext; 5s default
      HTTP timeout.
- [x] Key provisioning: at operator bootstrap (documented in `docs/local-
      dev.md` #Meilisearch key provisioning), use the master key once to
      create a read-only key (actions: `search`) and a write key (actions:
      `documents.*`, `indexes.*`, `settings.*`). Keys are secrets; master
      key never flows to running pods.
- [x] Health probe on the API: `ReadinessProbe` pinging `/health`
      endpoint. Logs degradation; readiness drops on failure but does not
      take the main API down — search failures degrade to 503 from the
      search endpoint only. Probe lives in
      `internal/search/meilisearch/probe.go`, wired in serve.go alongside
      the Postgres probe.

#### Success Criteria

- `MEILI_URL=http://localhost:7700 MEILI_API_KEY=... rfc-api serve` starts
  cleanly, readiness reflects Meili status.
- Killing Meili locally degrades `/api/v1/search` to 503 problem+json but
  `/api/v1/docs` continues to serve.
- Write key never logged / exposed in /metrics / /healthz responses.

---

### Phase 2: Index schema and settings

Lock the per-index configuration so ingest writes are compatible with the
read surface.

#### Tasks

- [x] Single index `documents` per OQ1; every indexed record has a `type`
      attribute for filtering. `IndexName = "documents"` lives in
      `internal/search/meilisearch/client.go`.
- [x] Settings bootstrap (idempotent): `searchableAttributes`: `title`,
      `section_heading`, `body_excerpt`; `filterableAttributes`: `type`,
      `status`, `labels`, `author_handles`, `visibility`;
      `sortableAttributes`: `created_at`, `updated_at`;
      `rankingRules`: defaults + `created_at:desc` near the bottom as a
      tiebreaker; `typoTolerance`: on; `displayedAttributes`: all.
      Declared in `DesiredSettings()` — `settings.go`.
- [ ] Extensions flattening: each key `k` in `document.extensions`
      becomes an indexed field `ext.<type_prefix>.<k>` (lowercased). Makes
      per-type filtered search cheap without leaking type-specific schema
      into the core. *Deferred to Phase 3 indexer (per-doc field shape).*
- [ ] Internal-network-only visibility flag: every indexed document gets
      `visibility: "internal"` until RFC-0001 Phase 4 hooks it to the
      authenticated caller's scopes. *Filterable attr is declared; the
      per-doc write happens in Phase 3 indexer.*
- [x] Settings migration: a small `ApplySettings()` routine idempotent
      against re-invocation; called on first worker start per
      [IMPL-0003][impl-0003] Phase 4 bootstrap path. Compares desired vs.
      current as sorted sets (ranking rules as ordered list) so restarts
      don't churn the server.

#### Success Criteria

- After worker first boot, `curl $MEILI_URL/indexes/documents/settings`
  matches the expected JSON exactly.
- Re-running `ApplySettings` is a no-op (no diff against the server).
- Filterable / searchable / sortable attribute lists are asserted by a
  unit test against a fresh container.

---

### Phase 3: Per-section indexing + worker-driven writes

The ingest pipeline (IMPL-0003 Phase 4) hands each document to this writer.
Split by heading hierarchy per ADR-0003 so hits resolve to sections, not
whole docs.

#### Tasks

- [x] `internal/search/meilisearch/indexer.go`: `Indexer{client,types}` with
      `Upsert(ctx, doc *domain.Document)` (single doc — ingest path) and
      `Delete(ctx, id domain.DocumentID)`. Upsert is delete-by-filter +
      re-add so a section lost between ingests leaves no orphan.
- [x] Per-section split: walk the Markdown AST (reusing goldmark from
      [IMPL-0004][impl-0004]), split into sub-documents at H1/H2
      boundaries (see OQ2 on depth). Each sub-doc's indexed id is
      `{document_id}#{section_slug}`; `parent_id` is the document id;
      `section_heading` carries the heading text; `body_excerpt` carries
      the prose under that heading up to ~500 chars. Extensions flatten
      as `ext_<prefix>_<key>`; every record carries `visibility: internal`
      per ADR-0003. `parent_id` added to filterableAttributes so
      delete-by-filter clears all sub-sections.
- [x] Batched writes: `indexBatchSize = 1024` keeps individual payloads
      in the low-MB range while letting a reindex drive Meili's task
      queue near its practical ceiling.
- [x] `reindex` job kind (enqueued by IMPL-0003 Phase 4 on every upsert)
      triggers the `Upsert` path; the worker consumes it. Handler lives
      in `internal/worker/reindex/reindex.go`; re-reads the source-of-
      truth Postgres row before re-indexing so the jobs table stores
      nothing larger than a document id.
- [x] Deletion propagation: scanner's tombstone path now enqueues a
      `search_delete` job (dedup `search-delete:<id>`); handler calls
      `Indexer.Delete` with the parent id and clears every sub-section.

#### Success Criteria

- A single document with three H2 sections produces 4 indexed docs (1
  head + 3 sections) after ingest.
- Deleting a document clears every sub-section too — no orphan hits.
- The fake-type test from [IMPL-0003][impl-0003] exercises this path:
  ingest produces search-able content retrievable via
  `/api/v1/search?q=…`.

---

### Phase 4: search.Client implementation + service.Search wiring

Replace the noop with real hits.

#### Tasks

- [x] `internal/search/search.go`: `Client` interface extended with
      `MatchedTerms`, `SectionHeading`, `SectionSlug` on `Result` per
      RD7. The NoopClient is retained for test harnesses.
- [x] `internal/search/meilisearch/query.go`: `Client.Query` honors
      `q`, `limit`, `cursor` (offset encoded as `base64(off:N)` per
      RD4), and the `type` filter when present. Always AND-constrains
      `visibility = "internal"` on the filter.
- [x] Translate highlights back into a client-visible shape: each hit
      carries `matched_terms` (dedup'd, lowercased) + rendered
      `<em>`-tagged snippet preferring `body_excerpt` > `title` >
      `section_heading`.
- [x] `internal/service/search.go` already plumbs `search.Query`
      through unchanged; swapping NoopClient → meilisearch.Client in
      `cmd/rfc-api/serve.go` was the wiring change.
- [ ] Update the contract test to reflect the real search response
      shape; keep it behind the `search` tag so old consumers (MCP
      tool) see the new fields additively.

#### Success Criteria

- `GET /api/v1/search?q=rate+limit` against a seeded corpus returns
  real hits, not an empty page.
- Per-type filter works: `GET /api/v1/search?q=…&type=adr` returns only
  ADR hits.
- Response shape validates against `api/openapi.yaml`; contract test
  still green.
- Latency: p95 search on a 1k-document seed under 50ms locally.

---

### Phase 5: Reindex command and reconciliation

An operator-run command that rebuilds the index from Postgres. Useful after
a settings change or index loss.

#### Tasks

- [ ] `rfc-api reindex` subcommand: iterates `documents` in Postgres,
      enqueues `reindex` jobs. Worker drains them through the Phase-3
      indexer.
- [ ] Online-safe: the running index keeps serving while rebuild
      happens; batched writes don't take a settings lock.
- [ ] Alternative (fully online swap): index into `documents_v2`,
      flip Meili alias to `documents`, delete the old — tracked in OQ5.
- [ ] Reconciliation: a scheduled job (or scanner extension) detects
      drift by counting rows in Postgres vs hits in Meili per type,
      re-enqueues missing ones. Log + emit a metric when drift > 0.
- [ ] `Makefile` target `make reindex` for local convenience.

#### Success Criteria

- `make compose-up && make reindex` against a seeded Postgres produces
  hits in Meili within a minute.
- Reindex is idempotent: running it twice doesn't change the index.
- Drift detection fires a Prometheus alert when the delta exceeds a
  configurable threshold (default: 1% of document count).

---

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/config/config.go` | Modify | Add `Meili` struct. |
| `internal/search/meilisearch/client.go` | Create | SDK wrapper + read/write clients. |
| `internal/search/meilisearch/indexer.go` | Create | Per-section Upsert/Delete. |
| `internal/search/meilisearch/settings.go` | Create | Idempotent settings bootstrap. |
| `internal/service/search.go` | Modify | Plumb SearchQuery through the real client. |
| `api/openapi.yaml` | Modify | Add search response shape (hits, highlights, facets). |
| `test/contract/contract_test.go` | Modify | Cover the new search shape. |
| `test/integration/search/` | Create | Meili integration tests via testcontainers. |
| `cmd/rfc-api/reindex.go` | Create | `rfc-api reindex` subcommand. |
| `cmd/rfc-api/main.go` | Modify | Dispatch `reindex`. |
| `Makefile` | Modify | `reindex` + search-specific test targets. |
| `CLAUDE.md` | Modify | Search operation + pitfalls. |

## Testing Plan

- **Unit** — query DSL translation, settings idempotency, section split
  correctness, extension key flattening.
- **Integration** — testcontainers-go with `meilisearch:v1`; seed a
  small corpus, exercise query variants, highlight rendering, filter
  combinations.
- **Contract** — existing `test/contract/` suite stays green after
  response-shape changes; new fields added additively.
- **Soak** — extend `make smoke-soak` with search queries against a
  seeded corpus; assert stable latency + no goroutine growth.

## Dependencies

- **ADR-0003** — decision record.
- **IMPL-0002** — source of truth for documents; reindex reads Postgres.
- **IMPL-0003** — ingest writes here; enqueues `reindex` jobs.
- **IMPL-0004** — Markdown AST walking is shared with the parser.

New Go modules:

- `github.com/meilisearch/meilisearch-go` — official SDK.
- `github.com/testcontainers/testcontainers-go/modules/meilisearch` or
  a hand-rolled module — testing.

## Open Questions

None at this time. See [#Resolved Decisions](#resolved-decisions).

## Resolved Decisions

1. **Single index, `type` filter attribute.** Matches ADR-0003.
   Per-type indexes are the right answer when per-type ranking rules
   or synonyms become a real pain — not now. Migrating single → per-
   type is a reindex, not a code change, so the cost is bounded and
   deferred cleanly. Extensions flatten as `ext.<type_prefix>.<key>`
   so wildly different schemas coexist without collision.
2. **Section-split at H1+H2.** Hits resolve to headings, not whole
   documents, per ADR-0003 #Ingest. Deeper fragments hits; shallower
   loses the heading UX. Matches Oxide's split shape.
3. **Domain-translated search response.** `{hits:[], total, facets}`
   shape; Meili's native `hits` / `estimatedTotalHits` /
   `processingTimeMs` does not leak to clients. Matches ADR-0003's
   "consumers never address Meili directly."
4. **Synthetic cursor encoding offset under the hood.** Clients see
   the same `Link: …rel="next"` convention as the rest of the list
   endpoints; Meili is offset-paginated underneath. Consistent
   cross-endpoint behavior wins over implementation elegance.
5. **In-place reindex for v1.** Upsert-by-id handles partial state
   gracefully and the corpus is small. Alias-swap is the right answer
   if reindex starts pinch-hitting serve latency — that's a
   future-IMPL concern, not a blocker.
6. **`body_excerpt` hardcoded at 500 chars.** Enough context for
   highlights without bloating the index. Make it configurable when
   a consumer has a concrete reason.
7. **Return both highlighted snippet and `matched_terms`.** The
   `<em>…</em>`-tagged snippet for HTML-rendering clients and a
   separate `matched_terms []string` for clients that don't render
   HTML (MCP tools, CLI). Costs a few bytes per hit; pays off.
8. **Long-lived read/write keys, rotated on secret changes.**
   Meili master key is bootstrap-only. No short-lived JWT-style
   tenant tokens in v1 — RFC-0001 Phase 4 OIDC will reshape auth
   anyway and a second key-lifecycle model before then is churn.

## References

- [ADR-0003: Use Meilisearch for rfc-api search][adr-0003]
- [RFC-0001: rfc-api][rfc-0001]
- [DESIGN-0002: DocumentType extensibility][design-0002]
- [IMPL-0001: HTTP server phase 1][impl-0001]
- [IMPL-0002: PostgreSQL store][impl-0002]
- [IMPL-0003: sync worker][impl-0003]
- [IMPL-0004: parser plugin seam][impl-0004]
- [INV-0001: Oxide RFD system case study][inv-0001]
- Meilisearch docs: <https://www.meilisearch.com/docs>

[adr-0003]: ../adr/0003-use-meilisearch-for-rfc-api-search.md
[rfc-0001]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
[design-0002]: ../design/0002-documenttype-extensibility-for-multiple-content-types.md
[impl-0001]: ./0001-rfc-api-http-server-phase-1-implementation.md
[impl-0002]: ./0002-rfc-api-postgresql-store-implementation.md
[impl-0003]: ./0003-rfc-api-sync-worker-implementation.md
[impl-0004]: ./0004-rfc-api-parser-plugin-seam-implementation.md
[inv-0001]: ../investigation/0001-oxide-rfd-system-architecture-case-study.md
