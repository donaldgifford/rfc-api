---
id: ADR-0002
title: "Use PostgreSQL as the rfc-api datastore"
status: Accepted
author: Donald Gifford
created: 2026-04-18
accepted: 2026-04-20
---
<!-- markdownlint-disable-file MD025 MD041 -->

# 0002. Use PostgreSQL as the rfc-api datastore

<!--toc:start-->
- [Status](#status)
- [Context](#context)
- [Decision](#decision)
- [Consequences](#consequences)
  - [Positive](#positive)
  - [Negative](#negative)
  - [Neutral](#neutral)
- [Alternatives Considered](#alternatives-considered)
- [References](#references)
<!--toc:end-->

## Status

Accepted — implementation landed in IMPL-0002; Postgres is the
production store end-to-end.

## Context

[RFC-0001: rfc-api][rfc-0001] describes a service that syncs Markdown
documents and their frontmatter from configured GitHub source repos and
serves them over a read-only JSON HTTP API. The service needs durable
storage for:

- Parsed documents keyed by id (e.g. `RFC-0011`, `ADR-0002`), including
  their frontmatter, body, and source-file pointer.
- Derived metadata: labels, statuses, authors, timestamps, and extracted
  cross-document references.
- Per-source sync bookkeeping: last-seen commit SHA, last-reconciled
  timestamp, per-document content hash.
- Listing, filtering, and joining across the above (list RFCs by status,
  fetch the set of documents that link to a given id, etc.).

The API is read-heavy. Writes are driven by the sync loop and are bounded
by GitHub activity, which is small in absolute terms. The store is a
**derived cache**: the Git repos are the source of truth, and a full
rebuild of the store from Git must always produce an equivalent state
(per RFC-0001's sync requirements).

The service targets the existing Kubernetes cluster, which already has
operational patterns, backup tooling, and on-call familiarity with
PostgreSQL.

## Decision

Use **PostgreSQL 18** as the persistent datastore for `rfc-api`.

- One logical database per `rfc-api` deployment, owned by the service.
- Schema is managed by migrations shipped with the service; migrations are
  applied on startup or by an explicit job (decided in the design doc).
- Major version pinned at **PostgreSQL 18** (released September 2025). The
  dev `compose.yaml` runs `postgres:18-alpine` so local SQL dialect, query
  plans, and available features match what the prod target runs against.
  Upgrades to subsequent majors are tracked as their own decisions.
- Full-text search is **not** committed to Postgres by this ADR. Search
  backend selection is resolved separately in
  [ADR-0003: Use Meilisearch for rfc-api search][adr-0003]; this ADR covers
  the primary document/metadata store only.

## Consequences

### Positive

- **Relational fit.** Documents, labels, cross-references, and sources are
  naturally modeled as related tables. Listing, filtering, and
  reference-graph queries are straightforward SQL rather than custom
  traversal code.
- **Operational familiarity.** Postgres is already run on the target
  cluster. Backup, restore, monitoring, and on-call practice all exist.
  No new storage substrate to learn or operate.
- **Strong consistency for the sync loop.** Transactions let the reconciler
  apply a per-source update atomically, so readers never observe a
  half-applied sync.
- **Headroom for later needs.** If we later want richer querying (JSONB on
  frontmatter, arrays for labels, `ltree` or recursive CTEs for link
  graphs, `pg_trgm` for lightweight text matching as a fallback), Postgres
  has the tools without adding another component.
- **Helm / Argo friendly.** Deploying alongside a managed or in-cluster
  Postgres is a solved pattern in the existing platform.

### Negative

- **Another stateful dependency to operate.** Even with existing practice,
  a per-service database adds backup scope, upgrade scheduling, and
  failure modes the service did not have before.
- **Full-text search on Postgres alone is limited.** `tsvector` and
  `pg_trgm` work for small corpora but do not match the UX of a dedicated
  search engine. Hence the explicit carve-out above: search backend is a
  separate decision.
- **Schema coupling across phases.** Adding new content types and new
  metadata will mean migrations. Forward-only, reviewed migrations are the
  expected discipline; ad-hoc schema changes in production are not.

### Neutral

- **Driver/ORM choice is deferred.** `database/sql` + `pgx`, `sqlc`,
  `ent`, or another data-access tool is a design-doc decision, not an
  ADR-level one.
- **Hosting mode is deferred.** In-cluster (e.g., CloudNativePG) vs.
  managed (e.g., a cloud-provider Postgres) is an operational decision
  recorded separately when we settle it.
- **The store is rebuildable.** Losing it is recoverable by re-syncing
  from Git. Backup policy can therefore lean on that property rather than
  treating the database as the sole source of truth.

## Alternatives Considered

1. **SQLite (embedded).** Tempting for a single-replica service with a
   rebuildable store. Rejected because we want horizontal headroom,
   predictable multi-replica rollouts on Kubernetes, and shared operational
   practice with other services. Also complicates backup/observability vs.
   Postgres.
2. **A document store (MongoDB, DynamoDB, etc.).** Each document is already
   a blob with frontmatter; a document DB fits superficially. Rejected
   because the interesting queries are relational (cross-references,
   label/status filters, per-source bookkeeping) and we would end up
   rebuilding secondary indexes. No operational advantage over Postgres in
   our environment.
3. **Keep state in memory; rebuild from Git on every start.** Viable for a
   tiny corpus. Rejected because startup time grows with corpus size and
   GitHub activity, and because webhook-driven incremental reconcile is a
   goal in RFC-0001.
4. **Put everything in the search engine (Meilisearch / OpenSearch).**
   Would collapse storage and search into one component. Rejected as the
   primary store: search engines are optimized for retrieval, not for
   transactional metadata updates, and we want the option to swap the
   search backend without touching the source of truth for documents.
5. **Object storage + an index file.** Cheap and simple for static
   snapshots. Rejected because the sync loop needs transactional updates
   and the API needs low-latency reads on the hot path.

## References

- [RFC-0001: rfc-api — Backend API for the Markdown Portal][rfc-0001]
- [RFC-0011: Markdown Portal][rfc-0011]
- [ADR-0001: Use Go and the standard library net/http for rfc-api][adr-0001]
- [ADR-0003: Use Meilisearch for rfc-api search][adr-0003]

[rfc-0001]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
[rfc-0011]: ../../INGEST_RFC.md
[adr-0001]: ./0001-use-go-and-stdlib-net-http-for-rfc-api.md
[adr-0003]: ./0003-use-meilisearch-for-rfc-api-search.md
