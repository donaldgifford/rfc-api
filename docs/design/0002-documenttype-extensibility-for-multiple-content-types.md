---
id: DESIGN-0002
title: "DocumentType: extensibility for multiple content types"
status: Draft
author: Donald Gifford
created: 2026-04-19
---
<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0002: DocumentType: extensibility for multiple content types

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-04-19

<!--toc:start-->
- [Overview](#overview)
- [Goals and Non-Goals](#goals-and-non-goals)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Background](#background)
- [Detailed Design](#detailed-design)
  - [The shape of a document type](#the-shape-of-a-document-type)
  - [The registry](#the-registry)
  - [Sources and types are independent](#sources-and-types-are-independent)
  - [Package and naming rule](#package-and-naming-rule)
  - [URL structure](#url-structure)
  - [Identifier format](#identifier-format)
  - [Pagination](#pagination)
  - [Parser plugin seam](#parser-plugin-seam)
  - [Schema extensions](#schema-extensions)
  - [Lifecycle states per type](#lifecycle-states-per-type)
  - [Cross-type concerns](#cross-type-concerns)
- [API / Interface Changes](#api--interface-changes)
- [Data Model](#data-model)
- [Testing Strategy](#testing-strategy)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Open Questions](#open-questions)
  - [Resolved open questions](#resolved-open-questions)
- [References](#references)
<!--toc:end-->

## Overview

`rfc-api` is a generic Markdown-corpus service, not an RFC-only one.
[RFC-0001][rfc-0001] anticipates multiple content types (RFC, ADR,
and future types like frameworks and style guides) and promises that
adding a type does not change the API contract. This design doc turns
that promise into a concrete code shape: a first-class `DocumentType`
abstraction, a type registry populated from config, and a per-type
URL structure (`/api/v1/{type}/{id}`) mounted from the registry at
startup — alongside a small cross-type aggregation surface
(`/api/v1/docs`, `/api/v1/search`) kept for corpus-wide queries.

The doc is published early, before any implementation, so the shape
can evolve in step with the first-pass build. Sections flagged as
open are expected to change; the principles in
[§Package and naming rule](#package-and-naming-rule) and
[§URL structure](#url-structure) are load-bearing and
should not drift without a follow-up.

## Goals and Non-Goals

### Goals

- Make "document type" a first-class concept in the code — a
  parameter threaded through services, storage, search, and
  handlers — rather than an afterthought expressed as a string
  column.
- Enable adding a new content type (for example: a style guide or a
  security framework) without touching handler, router, storage, or
  search-query code.
- Keep the per-type surface (`/api/v1/{type}/{id}`) registry-driven
  so adding, renaming, or removing a type is a router-level change
  only. Preserve cross-type aggregation (`/api/v1/docs`,
  `/api/v1/search`) for corpus-wide consumers (MCP, activity
  feeds).
- Capture the principled design constraints now so implementation
  decisions don't accidentally paint us into a corner.

### Non-Goals

- **Picking the specific set of content types for v1.** RFC-0001
  says RFC and ADR are v1; a third type lands in Phase 3 per
  RFC-0001. This doc describes *how* types plug in, not *which* we
  ship.
- **Defining frontmatter schemas for each type.** That lives with
  each type's own conventions (in the content repo) and the
  corresponding parser. This doc defines the registration seam,
  not the content.
- **Per-type UI rendering.** Owned by
  [RFC-0002: rfc-site][rfc-0002]; `rfc-api` returns raw Markdown
  and type metadata, the frontend decides how to render each type.
- **Type-to-type transformations** (e.g., converting an RFC into an
  ADR). Out of scope; if it comes up it is a content-repo workflow,
  not an API feature.
- **Multiple types per document.** A document is exactly one type
  at a time. Retyping is a content-repo operation (a move or
  frontmatter edit), not an API operation.
- **Completeness.** This doc is deliberately published early to
  evolve with implementation. Expect revisions; track them via
  git history and by updating the status in the frontmatter when
  the shape stabilizes.

## Background

The framing, in one sentence: **a GitHub repo has Markdown files, we
point to them, process them into our system to be searchable across
all types, and expose them via an API.**

RFC-0001 already commits to the generic-document shape:

- "A **content source** is a GitHub repo (or a path within one) plus
  a content type."
- "Different content types are parsed by type-specific logic but
  expose the same document shape."
- "Adding a future type (frameworks, runbooks, etc.) does not change
  the API contract."

What it does not commit to is the URL structure, the parser-plugin
seam, or the shape of type-specific metadata on the wire. Those are
the subject of this doc.

The tension worth naming up front: different content types will
have different Markdown conventions, different frontmatter fields,
and different UI rendering in `rfc-site`. They are not
interchangeable at the *content* level. The payoff of treating them
uniformly at the *API* level is that search, cross-references, MCP
tooling, and the operational surface stay simple — the frontend
decides where the types diverge.

## Detailed Design

### The shape of a document type

A `DocumentType` is a value object that describes everything the
system needs to know to ingest, store, search, and serve content of
a given type. Sketch (not final):

```go
// internal/domain/documenttype.go
package domain

type DocumentType struct {
    // Stable string identifier used throughout the system.
    // Examples: "rfc", "adr", "framework", "style-guide".
    ID string

    // Human display name for the type, e.g. "Request for Comments".
    Name string

    // ID prefix used in document identifiers — RFC-0001, SEC-0005.
    // Unique across types in the same deployment.
    Prefix string

    // Which content source backs this type, and where within it.
    // Exact shape deferred; see §Sources and types are independent.
    Source SourceRef

    // The lifecycle states valid for this type, in rough order
    // (e.g. for RFC: Draft → Proposed → Accepted → Superseded).
    Lifecycle []Status

    // Hook into the parser for this type's frontmatter + body.
    // The parser's contract is the generic Document shape;
    // type-specific fields land in Document.Extensions.
    Parser Parser
}
```

A `Document` carries a reference to its type and a loosely-typed
extensions bag for type-specific frontmatter:

```go
type Document struct {
    ID          string              // "RFC-0001"
    Type        string              // "rfc" — matches DocumentType.ID
    Title       string
    Status      Status
    Author      string
    CreatedAt   time.Time
    Body        string              // raw Markdown
    Links       []Link
    Labels      []string
    Extensions  map[string]any      // type-specific frontmatter
    Source      SourceRef           // repo + path + commit sha
}
```

Two points:

- The **core shape** (ID, title, status, author, created, body,
  links, labels, source) is the same across types. This is the
  API-uniformity promise.
- The **extensions bag** carries type-specific frontmatter without
  leaking it into the core shape. A future framework type's
  "compliance controls" list lives in `Extensions["controls"]` and
  is understood by the framework parser, the rfc-site framework
  view, and any MCP tool that asks for it. Nothing else in the
  pipeline needs to know about it.

### The registry

At startup, `rfc-api` loads a `DocumentTypeRegistry` from config:

```go
// internal/domain/registry.go
package domain

type DocumentTypeRegistry interface {
    Get(id string) (DocumentType, bool)       // by type id ("rfc")
    ByPrefix(prefix string) (DocumentType, bool) // by id prefix ("RFC")
    List() []DocumentType
}
```

Configuration is declarative:

```yaml
# config.yaml (sketch — final shape lives in config design)
document_types:
  - id: rfc
    name: Request for Comments
    prefix: RFC
    source:
      repo: donaldgifford/rfc-repo
      path: docs/rfc
    lifecycle: [Draft, Proposed, Accepted, Rejected, Superseded]
    parser: docz-markdown
  - id: adr
    name: Architecture Decision Record
    prefix: ADR
    source:
      repo: donaldgifford/rfc-repo
      path: docs/adr
    lifecycle: [Proposed, Accepted, Deprecated, Superseded]
    parser: docz-markdown
  - id: framework
    name: Security Framework
    prefix: SEC
    source:
      repo: donaldgifford/security-frameworks
      path: frameworks
    lifecycle: [Draft, Adopted, Retired]
    parser: framework-markdown
```

Adding a new type is a config change plus (possibly) a new parser
entry. It does not touch handler, router, storage, or search-query
code.

### Sources and types are independent

A content source and a document type are separate concepts that
happen to usually map one-to-one. The design allows both:

- **One repo, one type** — `rfc-repo` → RFCs only.
- **One repo, multiple types** — a single "docs" repo with
  `docs/rfc/`, `docs/adr/`, `docs/style-guide/` sub-paths, each
  mapped to its own type entry in config. Label-style
  discrimination becomes a config question, not an API shape
  question.
- **Multiple repos per type** — rarer, but expressible.

The type registry is the source of truth for "what types exist";
source configuration decides "where their content lives."

### Package and naming rule

**Type is a parameter, not a package name.**

This is the load-bearing constraint. Concrete rules:

- No package under `internal/` is named after a specific type.
  `internal/rfc/` is forbidden; `internal/docs/`, `internal/types/`,
  or `internal/handler/docs.go` are fine.
- No handler function is named `ListRFCs` or `GetFramework`. The
  handler is `Docs.List`, `Docs.Get`; type, if relevant to the
  handler, arrives as a filter parameter.
- No database column or search-index field is named `rfc_*`. Use
  `document_*` and filter by the `type` column/attribute.
- No storage helper is hardcoded to a specific type. If you find
  yourself writing `storeRFC`, stop — the function wants a
  `DocumentType` argument.

The reason is simple: any time you hardcode a type into a name, you
have committed to "this function only exists for RFCs" and adding a
second type means duplicating or renaming it. Both are the refactor
this doc is trying to prevent.

### URL structure

The API surface is **per-type URL prefixes, registry-mounted, with
a small cross-type aggregation surface kept alongside**:

```
Cross-type (aggregation / corpus-wide):
  GET  /api/v1/sources                   configured content sources
  GET  /api/v1/docs                      paginated list across all types
  GET  /api/v1/search                    cross-type full-text search

Per-type (one set mounted per DocumentType in the registry):
  GET  /api/v1/{type}                    paginated list of that type
  GET  /api/v1/{type}/{id}               single document
  GET  /api/v1/{type}/{id}/links         cross-references (in + out)
  GET  /api/v1/{type}/{id}/discussion    PR review comments / conversations
  GET  /api/v1/{type}/{id}/revisions     revision history (Phase 2+)
  GET  /api/v1/{type}/{id}/authors       author metadata

Webhook:
  POST /api/v1/webhooks/github           signed GitHub webhook receiver
```

Conventions:

- `{type}` is a lowercase type id from the registry (`rfc`, `adr`,
  `framework`, `style-guide`). Must be a known type or the request
  404s at the router.
- `{id}` is the **numeric portion only**, zero-padded (`0001`,
  `0042`). It is unique within a type, not globally. The URL
  `/api/v1/rfc/0001` refers to the canonical-display document
  `RFC-0001`.
- `{id}` casing is canonical (lowercase numeric; no alphabetic
  component in v1). Case sensitivity is trivially a non-issue for
  numeric strings.
- All list endpoints — `/docs` and `/{type}` — are paginated
  (see [§Pagination](#pagination)).

Why this shape:

- **Human- and LLM-readable URLs.** `/api/v1/rfc/0001` is
  self-documenting in a way a query-param (`/api/v1/docs?type=rfc`)
  is not. Chosen over the unified-with-query-param surface in
  review.
- **Per-type extensions are natural.** A framework type that ever
  needs `/controls` lives at `/api/v1/framework/{id}/controls`
  without contorting a universal `/docs` namespace. These
  extensions are additive and selectively mounted — most types
  never need them.
- **Per-type scopes are natural.** If a future type needs a
  distinct auth scope (`framework:read`), it maps cleanly onto the
  URL prefix.
- **Cache/rate-limit keying is cleaner** on URL paths than on
  query strings.
- **Cross-type aggregation is preserved** via `/api/v1/docs` and
  `/api/v1/search`. MCP and activity-feed consumers still have a
  single surface for corpus-wide queries.

Registry-driven mounting: at server startup `router.go` iterates
the registry and mounts the per-type endpoints under
`/api/v1/{type}/*` for every registered `DocumentType`. Adding a
new type = a config entry + (possibly) a new parser; no handler or
router code changes. Implementation lives in
[DESIGN-0001 §Route registration][design-0001-routing].

### Identifier format

Two id forms coexist and are used in different places. They are
mechanically interconvertible, and the system is strict about which
form is allowed where:

| Form             | Example    | Used in                                    |
|------------------|------------|--------------------------------------------|
| URL id           | `0001`     | URL path segments only                     |
| Canonical display id | `RFC-0001` | JSON response bodies, cross-references, logs, search hits, docz filenames, anywhere the id appears to a human |

Conversion is: `display_id = strings.ToUpper(type.Prefix) + "-" + urlID`.
The reverse (display → URL) is a split on `-` plus lowercase of
the prefix.

The storage key is the canonical display id (a single string,
globally unique). The URL id is a convenience for URL-path
cleanliness; no code path relies on it being stored as a separate
field.

**On the read hot path the registry is not consulted.** A request
`GET /api/v1/rfc/0001` resolves as:

```
handler: type = r.PathValue("type"); urlID = r.PathValue("id")
       : displayID = canonicalDisplayID(type, urlID) // e.g. "RFC-0001"
svc    : Docs.Get(ctx, displayID)
store  : SELECT * FROM documents WHERE id = $1
```

The stored row carries its type as a column; the handler does not
parse a prefix from the id and does not call the registry to
dispatch. The registry's jobs are:

- Config validation at startup (reject duplicate prefixes).
- Populating `/api/v1/sources` response metadata.
- Driving the route-mount loop at startup.
- Ingest-time parser dispatch in the worker.

### Pagination

All list endpoints (`/api/v1/docs`, `/api/v1/{type}`, and future
list-shaped endpoints) are paginated. Per DESIGN-0001 §Resolved
Decisions, list responses are bare JSON arrays with pagination
metadata in headers:

- Query parameter `limit` (default 50, max 200).
- Query parameter `cursor` — opaque, server-issued, stable.
- Response headers:
  - `X-Total-Count` — total matching documents (not just page size).
  - `Link` per [RFC 8288][rfc-8288] with `rel="next"` and
    `rel="prev"` where applicable.

Cursor rather than offset is chosen for stability: an ongoing
ingest can add or supersede documents while a client is paging;
cursor-based navigation does not skip or duplicate on those
events. Cursor contents are an implementation detail (probably a
base64-encoded `(last_id, last_ingested_at)` tuple) and must never
be relied on by clients.

[rfc-8288]: https://datatracker.ietf.org/doc/html/rfc8288
[design-0001-routing]: ./0001-rfc-api-http-server-go-net-http-structure.md#route-registration

### Parser plugin seam

Parsers implement a small interface:

```go
// internal/domain/parser.go
package domain

type Parser interface {
    // Parse a single Markdown file's contents into a Document.
    // The caller supplies the DocumentType so the parser can
    // interpret type-specific frontmatter. Returns a domain
    // error on malformed input; parser errors are never
    // silent.
    Parse(raw []byte, t DocumentType, src SourceRef) (Document, error)
}
```

Parsers live in `internal/parser/` (not under a type-named package).
Registration is via config name — `parser: docz-markdown`,
`parser: framework-markdown` — mapping to concrete implementations
at startup.

**Open:** whether parsers are compiled in (one registry in Go
code), plugin-loaded (`.so` dynamic load — not Go-idiomatic), or
external processes (stretch). Default for v1: compiled in, with a
clean registration point so external parsers are a future option.

### Schema extensions

Per-type frontmatter lands in `Document.Extensions map[string]any`.

API exposure: the `/api/v1/{type}/{id}` response includes an
`extensions` object carrying whatever the type's parser populated.
Clients that care about specific types' extensions (rfc-site's
framework view, an MCP tool filtering for a specific control) read
from `extensions.*`. Type-specific sub-resources
(`/api/v1/framework/{id}/controls`) are a second way to expose the
same data in a more constrained shape when a type has earned its
own endpoint.

Search index: extensions are flattened into indexable fields
(namespaced by type prefix, e.g. `framework.controls.id`) so
per-type filtered search is possible without bloating the core
schema.

**Open:** whether extensions are fully open-ended or
schema-validated per-type. v1 default is open-ended; revisit when
a consumer starts relying on specific extension fields existing.

### Lifecycle states per type

Each `DocumentType` declares its own `Lifecycle []Status` vocabulary.
A document's status must be one of the values in its type's
lifecycle.

- RFC: `Draft, Proposed, Accepted, Rejected, Superseded`.
- ADR: `Proposed, Accepted, Deprecated, Superseded`.
- Future framework type might use `Draft, Adopted, Retired`.

The API surfaces `status` as a free string at read time (clients do
not need the vocabulary); the worker enforces the per-type
vocabulary at ingest time.

### Cross-type concerns

Features that span all types must not grow type-awareness beyond
the `DocumentType` parameter:

- **Search.** The Meilisearch index holds documents from every
  type with a `type` attribute. `/api/v1/search` filters via
  `filter=type=rfc` when the caller narrows, otherwise returns
  cross-type hits.
- **Cross-type list.** `/api/v1/docs` returns documents across
  all types, paginated, optionally narrowed by `?type=`. Used by
  activity feeds, cross-corpus MCP tooling, and any consumer that
  wants "everything".
- **Cross-references / links.** A reference from an RFC to a
  framework is just a link between two `Document` rows. The
  `/api/v1/{type}/{id}/links` endpoint returns both incoming and
  outgoing references; those references **can and do cross
  types** — an RFC can link to a framework, and vice versa. The
  handler does not special-case types, and cross-type linking is
  not restricted by the API. Each reference in the response
  carries its own `{type, id, href}`.
- **MCP server.** Exposes both a cross-type tool set
  (`list_docs`, `search`) and per-type tools (`get_rfc`,
  `get_framework`) as the corpus grows. Exact shape is an MCP
  design-doc concern; the API gives it both surfaces.

## API / Interface Changes

The API is **per-type URL prefixes plus a cross-type aggregation
surface**, registry-driven. The full endpoint set is defined in
[RFC-0001 §API surface][rfc-0001-api] and
[DESIGN-0001 §API / Interface Changes][design-0001-api]; this doc
owns the rules those endpoints must obey:

- `GET /api/v1/{type}` and `GET /api/v1/{type}/{id}` are mounted
  by iterating the registry at startup. Adding a type = adding a
  config entry; no route-code change.
- `GET /api/v1/docs` and `GET /api/v1/search` stay as cross-type
  aggregation endpoints. Both accept `?type=` for narrowing. Both
  are paginated (see [§Pagination](#pagination)).
- `GET /api/v1/sources` response includes each source's declared
  type.
- Per-type sub-resources (`/{type}/{id}/links`,
  `/{type}/{id}/discussion`, `/{type}/{id}/revisions`,
  `/{type}/{id}/authors`) are mounted uniformly across all
  registered types. Truly type-specific endpoints (e.g.,
  `/framework/{id}/controls`) are additive and register only for
  types that declare them.

**Config changes:** adds a `document_types` section (shape
sketched above; final lives in a config design doc). Present from
v1 with `rfc` and `adr` entries — no "before types" vs "after
types" config migration.

## Data Model

Storage owns schema; this doc only states constraints the storage
design must honor:

- Documents keyed by a globally unique **canonical display id**
  (`RFC-0001`, `SEC-0005`). This is one string column, not a
  composite key. The URL numeric id (`0001`) is derived for URL
  construction only; it is not stored independently.
- A `type` column is present on every document row and every
  search-index document. Indexed, filterable. Populated by the
  worker at ingest time from the document's source; never derived
  from the id at read time.
- Per-type extensions are stored either in a JSONB column on the
  document row or in a small side table per type. Decision
  deferred to the storage design doc under [ADR-0002][adr-0002].
- Lifecycle state is stored as a free string, with per-type
  vocabularies enforced in application code (not by a database
  enum that would have to grow every time we add a type).
- **Identifier prefix uniqueness is enforced at registry load.**
  Two types sharing a prefix is a config error and refuses to
  start the service. See [§Open Questions](#open-questions).

## Testing Strategy

Type extensibility is validated by a cross-cutting test:

1. **The "fake type" test.** The test suite registers a contrived
   `DocumentType` (`id: test-type`, `prefix: TST`) with its own
   minimal parser and exercises the full path: ingest → store →
   search → API read → cross-reference. This runs on every push.
   If it fails when a new type is added, the type abstraction has
   leaked somewhere it shouldn't have.
2. **Service tests stay type-agnostic.** `service.Docs` tests pass
   the test type alongside RFC, and assert both are handled
   identically by the same code.
3. **Naming-rule enforcement.** A CI check greps for forbidden
   patterns (`internal/rfc/`, `RFC` in Go identifier names outside
   the registry) and fails if they appear. Cheap to implement;
   keeps the naming rule honest.

## Migration / Rollout Plan

This design is greenfield alongside the service itself. Rollout
tracks [RFC-0001][rfc-0001] phases:

1. **Phase 1 (RFC + ADR).** Registry contains two entries, both
   using the same `docz-markdown` parser. Proves the abstraction
   with two types from one repo without exercising the harder
   dimensions (multiple repos, custom parsers).
2. **Phase 2 (search, discussions).** Confirms search and
   cross-reference code does not grow type-awareness.
3. **Phase 3 (third content type).** The real validation. Onboards
   a type that is not RFC/ADR, ideally one that differs on
   frontmatter schema, lifecycle, and source repo. This is where
   the "fake type" test graduates into a real type and the
   abstraction either holds or breaks. If it breaks, we revise this
   doc, bump its status, and patch the seam.
4. **Phase 4+.** Type-specific sub-resources (`/controls`, etc.)
   added selectively for types that declare them; OIDC auth
   lands per RFC-0001. Per-type auth scopes revisited then.

## Open Questions

Still open; expected to evolve with implementation:

1. **Parser plugin seam.** Compiled-in vs external plugin vs
   subprocess. Default: compiled in for v1, clean registration
   point for future.
2. **Extensions schema enforcement.** Open-ended `map[string]any`
   vs per-type JSON schema validation. Default: open-ended until a
   consumer cares.
3. **Identifier generation.** Who assigns `RFC-0042`? Assume the
   content repo does (via `docz` or convention) for now; revisit
   if collisions become a real problem.
4. **Retyping a document.** If a file moves from `docs/rfc/` to
   `docs/adr/`, is that a new document or the same document? v1
   default: new document with a new id, old one goes to
   `Superseded`. Revisit.
5. **MCP tool surface — how much per-type convenience.** The API
   exposes both cross-type (`/docs`, `/search`) and per-type
   (`/{type}/{id}`) surfaces; the MCP server can present a
   generic tool set, a per-type tool set, or both. Exact shape is
   a later MCP design-doc concern. Default: both, with per-type
   tools added as corpus grows.
6. **Type deprecation.** When a type is removed from config, what
   happens to its documents? Default: stay accessible read-only
   until explicitly purged. Not a v1 concern.

### Resolved open questions

- **Identifier collisions across types → firm constraint.** Prefix
  uniqueness is enforced at registry startup; two types sharing a
  prefix is a config error and refuses to start the service. (Was
  "default for v1, revisit if it comes up.") See
  [§Data Model](#data-model).
- **Per-type rate limits and auth scopes → single scope for v1.**
  All types share one rate limit and a single `docs:read` scope
  per DESIGN-0001 Phase 4. If a future type requires a distinct
  scope, the per-type URL prefix is the natural slot to enforce
  it on. Revisit when a concrete need lands.
- **Cross-type cross-references → allowed.** `/links` does not
  filter or restrict by type; an RFC can link to a framework and
  vice versa. Responses carry `{type, id, href}` per reference so
  clients can render them consistently.
- **URL shape → per-type prefix + numeric id, cross-type
  aggregation preserved.** `/api/v1/{type}/{id}` is the primary
  surface, alongside `/api/v1/docs` and `/api/v1/search` for
  cross-type queries. See
  [§URL structure](#url-structure). (Was a reserved future
  option; promoted to the day-one design in review.)

## References

- [RFC-0001: rfc-api — Backend API for the Markdown Portal][rfc-0001]
  — parent RFC; §Scope and §Content model commit to multi-type
  support.
- [RFC-0002: rfc-site — Web Frontend for the Markdown Portal][rfc-0002]
  — consumer of per-type metadata; owns type-specific rendering.
- [RFC-0011: Markdown Portal][rfc-0011] — portal-level framing;
  anticipates multiple content types from day one.
- [ADR-0001: Use Go and the standard library net/http for rfc-api][adr-0001]
- [ADR-0002: Use PostgreSQL as the rfc-api datastore][adr-0002]
- [ADR-0003: Use Meilisearch for rfc-api search][adr-0003]
- [DESIGN-0001: rfc-api HTTP server — Go + net/http structure][design-0001]
  — sibling design doc; implements the naming rule and router
  conventions captured here.
- [INV-0001: Oxide RFD system — architecture case study][inv-0001]
  — Oxide's system is single-type (RFDs only); this doc goes
  beyond Oxide by treating type as a first-class variable.

[rfc-0001]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
[rfc-0002]: ../rfc/0002-rfc-site-web-frontend-for-the-markdown-portal.md
[rfc-0011]: ../../INGEST_RFC.md
[adr-0001]: ../adr/0001-use-go-and-stdlib-net-http-for-rfc-api.md
[adr-0002]: ../adr/0002-use-postgresql-as-the-rfc-api-datastore.md
[adr-0003]: ../adr/0003-use-meilisearch-for-rfc-api-search.md
[design-0001]: ./0001-rfc-api-http-server-go-net-http-structure.md
[inv-0001]: ../investigation/0001-oxide-rfd-system-architecture-case-study.md
