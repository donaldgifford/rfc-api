---
id: ADR-0003
title: "Use Meilisearch for rfc-api search"
status: Proposed
author: Donald Gifford
created: 2026-04-18
---
<!-- markdownlint-disable-file MD025 MD041 -->

# 0003. Use Meilisearch for rfc-api search

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

Proposed

## Context

[RFC-0001: rfc-api][rfc-0001] requires full-text search over the
document corpus. The [original RFC-0001 draft][rfc-0001] left two
questions open:

1. Which search engine.
2. Where search lives — inside `rfc-api`, inside `rfc-site` (the
   frontend), or as a sibling service consumed by both.

[INV-0001: Oxide RFD system architecture case study][inv-0001] resolved
the locational question in favour of "behind the API": Oxide puts
Meilisearch behind `rfd-api`, with the async worker
(`rfd-processor`) populating the index on each revision, and the
frontend querying through the API. The payoff is that the MCP server,
the CLI, and any other programmatic consumer get search "for free"
through the same contract the frontend uses — there is a single
canonical surface over the corpus.

We want the same property for our system. That means search must live
behind `rfc-api`. This ADR picks the engine and records the ingest +
query topology.

## Decision

Use **Meilisearch** as the full-text search engine for `rfc-api`,
deployed alongside the service on Kubernetes.

- **Ingest.** The `rfc-api` sync worker updates the Meilisearch index
  as part of each document-ingest job. Documents are split into
  sub-sections (heading hierarchy) and indexed per-section so that
  search hits resolve to headings, not whole documents.
- **Query.** `rfc-api` exposes `GET /api/v1/search?q=…` and proxies
  the query to Meilisearch. **Consumers never address Meilisearch
  directly** — not the frontend, not the MCP server, not the CLI.
- **Transport.** The API holds the Meilisearch API key. Read-only and
  write-scoped keys are separate; the worker holds the write key, the
  API holds a read-only key.
- **Deployment.** Meilisearch runs as a StatefulSet in the same
  Kubernetes namespace as `rfc-api`, packaged in the service's Helm
  chart, with persistent storage. Backup is out-of-scope — the index
  is fully rebuildable from Postgres (and Postgres is fully
  rebuildable from Git).
- **Tenancy.** A single index per environment, with per-document
  visibility flags in the index document so future visibility rules
  (public vs. internal) can be enforced by filter, without touching
  the ingest schema.

## Consequences

### Positive

- **Single canonical surface.** All consumers hit `/api/v1/search`
  and get identical results, including MCP and any future
  tooling. No "frontend has search, nothing else does" asymmetry.
- **Operational simplicity.** Meilisearch is a single binary with a
  single on-disk store. No cluster coordination, no JVM, no
  broker dependency. Fits our "no exotic dependencies" constraint.
- **Good defaults for a small corpus.** Typo tolerance, prefix
  search, and highlighting work out of the box. The relevance is
  acceptable for a document portal without tuning — and is tunable
  later via synonyms, stop words, and ranking rules.
- **Prior-art alignment.** Oxide uses Meilisearch for the RFD
  system; the shape we're copying has been validated at their
  scale.
- **Rebuildable.** Losing the Meilisearch volume is not an
  incident: worker replays the corpus from Postgres. This frees
  us from a backup discipline for the index.

### Negative

- **Another stateful service to operate.** A persistent-volume
  StatefulSet in the same namespace, with its own upgrades and
  disk sizing. Mitigated by Meilisearch being genuinely low-op
  for a single-instance deployment.
- **Scaling ceiling.** Meilisearch scales vertically; horizontal
  scaling is limited without Meilisearch Cloud. Not a concern
  for a document corpus at our projected size; would be if we
  tried to reuse it for a log-search or large-catalogue workload.
- **Facet and analytics queries are weak relative to OpenSearch /
  Elasticsearch.** We do not need those today, but a future pivot
  (e.g., an analytics dashboard over documents) might push us
  off Meilisearch.
- **API proxying adds one hop.** Search queries pass through
  `rfc-api` instead of going direct to the index. Latency cost
  is sub-millisecond in-cluster; acceptable for the benefit of
  a single canonical surface.

### Neutral

- **Visibility filtering** is a stored field, not (yet) a query
  constraint. v1 corpus is internal-only so the flag is
  ornamental until auth lands; will be enforced by query filter
  or by scoped API keys in Phase 4 of RFC-0001.
- **Per-section indexing schema** is fixed to "heading-hierarchy
  sub-sections" at ingest time. Reshaping the schema requires a
  full reindex. Full reindex is a routine operation in our
  model; not a concern.
- **Meilisearch Cloud vs. self-hosted** is deferred. We start
  self-hosted on the existing cluster; revisit if operational
  cost becomes a real line item.

## Alternatives Considered

1. **PostgreSQL full-text search (`tsvector` + `pg_trgm`).**
   Attractive because [ADR-0002][adr-0002] already commits to
   Postgres. Rejected for the v1 UX: ranking, typo tolerance, and
   prefix-search with highlighting are enough more work on
   Postgres to matter, and the ergonomic gap compounds as the
   corpus grows. We keep Postgres full-text as a fallback for
   lightweight internal queries if we ever need a search path
   that does not depend on Meilisearch being up.
2. **OpenSearch / Elasticsearch.** Technically capable; too
   heavyweight. JVM, cluster coordination, and the operational
   posture they imply are disproportionate for a document
   portal's expected corpus size. Revisit only if we need facets,
   analytics, or a shared cluster that already exists for other
   workloads.
3. **Typesense.** Closest peer to Meilisearch by design
   philosophy. Similar ergonomics, similar deployment shape.
   Rejected on the narrower grounds of prior-art fit — Oxide's
   model uses Meilisearch and we are explicitly borrowing that
   architecture.
4. **Algolia (SaaS).** Best-in-class DX and relevance. Rejected on
   data residency (internal docs leaving the cluster) and on cost
   for a use case that does not justify a SaaS line item.
5. **Search deployed with `rfc-site` (frontend-owned).** Rejected.
   Would fragment programmatic access (MCP/CLI would need a
   second integration), would push search-backend concerns into
   a frontend team, and would contradict the "API is the
   canonical surface" principle in RFC-0001. Oxide's design and
   [INV-0001][inv-0001] both argue for backend-owned search.
6. **Search as a sibling service consumed by both API and site.**
   Rejected. Adds an authorization surface (who can query the
   raw index?) and a contract-versioning problem (who owns the
   query DSL?). The proxy-through-API model collapses both.

## References

- [RFC-0001: rfc-api — Backend API for the Markdown Portal][rfc-0001]
- [RFC-0011: Markdown Portal][rfc-0011]
- [INV-0001: Oxide RFD system — architecture case study][inv-0001]
- [ADR-0001: Use Go and the standard library net/http for rfc-api][adr-0001]
- [ADR-0002: Use PostgreSQL as the rfc-api datastore][adr-0002]
- [Meilisearch documentation](https://www.meilisearch.com/docs)

[rfc-0001]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
[rfc-0011]: ../../INGEST_RFC.md
[inv-0001]: ../investigation/0001-oxide-rfd-system-architecture-case-study.md
[adr-0001]: ./0001-use-go-and-stdlib-net-http-for-rfc-api.md
[adr-0002]: ./0002-use-postgresql-as-the-rfc-api-datastore.md
