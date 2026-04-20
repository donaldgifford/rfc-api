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

- [ ] Pick the SDK (default: `github.com/meilisearch/meilisearch-go`,
      the official client).
- [ ] `internal/config/config.go`: add `Meili` struct with `URL string`
      (upstream-standard: `MEILI_URL`), `MasterKey string`
      (`MEILI_MASTER_KEY`, already reserved), `APIKey string`
      (`MEILI_API_KEY` — read-scoped for the API), `WriteKey string`
      (`MEILI_WRITE_KEY` — scoped for the worker). Env-var naming follows
      the memory rule (upstream name unchanged for external deps).
- [ ] `internal/search/meilisearch/client.go`: `Client{c *meilisearch.
      Client}`. Constructors `NewReadClient(cfg)` and `NewWriteClient(cfg)`
      that pick the right key.
- [ ] Key provisioning: at operator bootstrap (documented in `docs/local-
      dev.md`), use the master key once to create a read-only key (actions:
      `search`) and a write key (actions: `documents.*`, `indexes.*`,
      `settings.*`). Keys are secrets; master key never flows to running
      pods.
- [ ] Health probe on the API: `ReadinessProbe` pinging `/health`
      endpoint. Logs degradation; readiness drops on failure but does not
      take the main API down — search failures degrade to 503 from the
      search endpoint only.

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

- [ ] Single index `documents` per OQ1; every indexed record has a `type`
      attribute for filtering.
- [ ] Settings bootstrap (idempotent): `searchableAttributes`: `title`,
      `section_heading`, `body_excerpt`; `filterableAttributes`: `type`,
      `status`, `labels`, `author_handles`, `visibility`;
      `sortableAttributes`: `created_at`, `updated_at`;
      `rankingRules`: defaults + `created_at:desc` near the bottom as a
      tiebreaker; `typoTolerance`: on; `displayedAttributes`: all.
- [ ] Extensions flattening: each key `k` in `document.extensions`
      becomes an indexed field `ext.<type_prefix>.<k>` (lowercased). Makes
      per-type filtered search cheap without leaking type-specific schema
      into the core.
- [ ] Internal-network-only visibility flag: every indexed document gets
      `visibility: "internal"` until RFC-0001 Phase 4 hooks it to the
      authenticated caller's scopes.
- [ ] Settings migration: a small `ApplySettings()` routine idempotent
      against re-invocation; called on first worker start per
      [IMPL-0003][impl-0003] Phase 4 bootstrap path.

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

- [ ] `internal/search/meilisearch/indexer.go`: `Indexer{client}` with
      `Upsert(ctx, docs []domain.Document) error` and `Delete(ctx, id
      domain.DocumentID) error`.
- [ ] Per-section split: walk the Markdown AST (reusing goldmark from
      [IMPL-0004][impl-0004]), split into sub-documents at H1/H2
      boundaries (see OQ2 on depth). Each sub-doc's indexed id is
      `{document_id}#{section_slug}`; `parent_id` is the document id;
      `section_heading` carries the heading text; `body_excerpt` carries
      the prose under that heading up to ~500 chars.
- [ ] Batched writes: Meili's bulk API handles up to 10k docs per call
      comfortably; batch appropriately.
- [ ] `reindex` job kind (enqueued by IMPL-0003 Phase 4 on every upsert)
      triggers the `Upsert` path; the worker consumes it.
- [ ] Deletion propagation: IMPL-0003 Phase 4's tombstone path enqueues
      a `search_delete` job which calls `Indexer.Delete` with the parent
      id and clears every sub-section.

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

- [ ] `internal/search/search.go`: `Client` interface (already exists for
      the noop); tighten / expand as needed — the current surface is
      minimal; see OQ3 on facet returns.
- [ ] `internal/search/meilisearch/client.go`: implement `Query(ctx,
      SearchQuery) (SearchResults, error)`. Honors `q`, `limit`, `cursor`
      (see OQ4 on cursor shape — Meili uses offset pagination), and
      `type` filter when present.
- [ ] Translate highlights back into a client-visible shape: each hit
      carries `matched_terms` + rendered `highlight` snippets for
      `title` + `body_excerpt`.
- [ ] Update `internal/service/search.go` to plumb the typed
      `SearchQuery` through; no handler changes (they read
      `routectx`/`r.Query` the same way).
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

1. **Single index with `type` filter vs per-type indexes.** ADR-0003
   says single + filter. Per-type indexes would let us tune settings per
   type (different synonyms, different ranking rules) but at the cost of
   a cross-type search having to fan out N queries. Default: stick with
   ADR-0003 (single index). Revisit only if we need per-type tuning.
2. **Section-split depth.** Options: split only at H1, at H1+H2, at all
   heading levels. Oxide splits at H1/H2. Default: H1+H2. Deeper splits
   fragment hits; shallower ones lose the "hit resolves to a heading"
   UX.
3. **Search response shape: Meili-native vs domain-translated.** Meili
   returns `hits`, `estimatedTotalHits`, `processingTimeMs`, etc. Our
   clients want a more stable shape. Default: translate into
   `{hits:[], total: int, facets: {}}` inside the API; don't leak
   Meili's shape to clients (per ADR-0003 "consumers never address Meili
   directly").
4. **Cursor vs offset pagination.** Meili uses offset; our existing list
   endpoints use cursor. For search, offset is fine (deep pagination
   isn't a real use case), but we'd break the headers convention.
   Default: use a synthetic cursor that encodes the offset — clients
   still see `Link: …rel="next"`, implementation is offset under the
   hood.
5. **Reindex strategy: in-place vs alias-swap.** In-place is simpler
   but a concurrent search can see partial state mid-reindex. Alias-swap
   is atomic but needs a Meili feature or doubled write load. Default:
   in-place for v1 because the corpus is small and upsert-by-id handles
   partial state gracefully; revisit if reindex starts pinch-hitting
   serving latency.
6. **Excerpt length for `body_excerpt`.** 500 chars gives enough context
   for highlights without bloating the index. Configurable? Default: a
   hardcoded 500 until a consumer has a real reason to want it tunable.
7. **Highlight rendering.** Meili returns highlighted text with
   `<em>…</em>` tags by default. Do we return that markup as-is
   (clients render it), or pre-strip and return `matched_terms` so
   clients can re-highlight? Default: return both — the raw snippet
   with tags and a separate `matched_terms` slice. Costs a few bytes
   per hit; pays off for MCP tools that don't render HTML.
8. **Meili auth-key lifetime.** Master key long-lived (bootstrap only);
   read + write keys long-lived too, rotated on secret changes.
   Should we support short-lived scoped keys (Meili supports JWT-style
   tenant tokens)? Default: no; overkill for v1, and RFC-0001 Phase 4
   OIDC will reshape auth anyway.

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
