---
id: DESIGN-0003
title: "listDocs filter and sort query parameter contract"
status: Draft
author: Donald Gifford
created: 2026-05-12
---
<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0003: listDocs filter and sort query parameter contract

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-05-12

<!--toc:start-->
- [Overview](#overview)
- [Goals and Non-Goals](#goals-and-non-goals)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Background](#background)
- [Detailed Design](#detailed-design)
  - [Query string shape](#query-string-shape)
  - [Filter semantics](#filter-semantics)
  - [Sort semantics](#sort-semantics)
  - [Cursor encoding under variable sort](#cursor-encoding-under-variable-sort)
  - [Total-count headers](#total-count-headers)
  - [Error contract](#error-contract)
- [API / Interface Changes](#api--interface-changes)
- [Data Model](#data-model)
- [Testing Strategy](#testing-strategy)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Open Questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Overview

Extend `GET /api/v1/docs` (operation `listDocs`) with two additional query
parameters — `?filter=` (repeatable, `field:value` shape) and `?sort=` (single
value, fixed enum) — so the rfc-site Directory Toolbar can mirror multi-select
type filters + a sort dropdown to the URL and have server-side pagination
remain coherent across that filtered/sorted view. The change is purely
additive: callers that pass neither parameter see the existing behavior
(unfiltered, `created_at DESC`).

## Goals and Non-Goals

### Goals

- Unblock [rfc-site issue #6 Phase 7b](https://github.com/donaldgifford/rfc-site/blob/main/docs/impl/0004-build-rfc-portal-components-per-inv-0002-inventory.md#L385): the `<DirectoryToolbar>` loader needs server-side `filter` + `sort` so pagination stays coherent across the filtered view.
- Define a contract that extends cleanly to non-type filters (`status:`, `author:`, ...) and additional sort keys without a breaking change.
- Preserve cursor-based pagination across the filtered/sorted view: `Link: rel=next` must round-trip the active `filter` + `sort` so subsequent page fetches stay inside the same scope.
- Surface counts the toolbar can use for "(N of M shown)" rendering.

### Non-Goals

- Modifying `listDocsByType` (`/api/v1/{type}/`). It stays path-scoped, single-type, sort-defaulted. The new contract is for the cross-type endpoint only.
- Modifying `searchDocs` (`/api/v1/search`). It already has `?type=` and inherits Meili's ranking; bolting `filter=` + `sort=` onto it is a separate conversation.
- Adding full-text search to `listDocs`. That's `searchDocs`'s job.
- Implementing every filter field rfc-site might want forever. Phase 1 of the consuming IMPL doc only needs `type:`; the *contract* must be open-ended, the *implementation* can ship one field at a time.

## Background

The cross-type list endpoint at `/api/v1/docs` currently exposes only
`?limit=` and `?cursor=`:

```yaml
# api/openapi.yaml
/api/v1/docs:
  get:
    operationId: listDocs
    parameters:
      - $ref: "#/components/parameters/Limit"
      - $ref: "#/components/parameters/Cursor"
```

The Postgres store `internal/store/postgres/docs.go` hard-codes
`ORDER BY created_at DESC, id ASC` across 5 paginated queries (one per
{filtered, unfiltered} × {first page, mid page} permutation). The cursor
package `internal/server/cursor` encodes `(created_at, id)` opaquely.

The path-scoped per-type list at `/api/v1/{type}/` already exists and works,
but rfc-site's directory page wants a *single* request that returns hits
across an arbitrary set of types (e.g. RFC + ADR + DESIGN selected
simultaneously), not three separate requests stitched together client-side.
Issue [#28](https://github.com/donaldgifford/rfc-api/issues/28) tracks the
contract change.

The corresponding upstream design decisions live in
[DESIGN-0002][design-0002] (`#Cross-type concerns`), which already notes
that `/api/v1/docs` is "optionally narrowed by `?type=`" — that wording is
loose and predates the concrete rfc-site UI needs; this design pins it.

[design-0002]: ./0002-documenttype-extensibility-for-multiple-content-types.md

## Detailed Design

### Query string shape

```
GET /api/v1/docs?filter=type:rfc&filter=type:adr&sort=updated_desc&limit=25&cursor=…
```

- `filter` is **repeatable**. Each value is the literal string `field:value`
  with a single ASCII colon as separator. The field name and value are each
  drawn from a constrained character set (see Filter semantics).
- `sort` is **single-valued**. Repeating it is an error (400).
- `limit` and `cursor` retain their existing semantics.

### Filter semantics

**Shape.** `filter=<field>:<value>` where:

- `<field>` matches `^[a-z][a-z0-9_]*$` (snake_case, lowercase ASCII, must
  start with a letter).
- `<value>` matches the field's own pattern — for `type:`, the existing
  document-type id pattern `^[a-z][a-z0-9-]*$` (validated against the live
  type registry).

**Within-field semantics.** Multiple values for the same field are **OR**.
`filter=type:rfc&filter=type:adr` returns RFCs and ADRs. (See [OQ1](#open-questions).)

**Cross-field semantics.** Different fields are **AND**.
`filter=type:rfc&filter=status:accepted` returns accepted RFCs. (Decided —
matches REST idioms and is what every consumer naturally expects.)

**Field set in Phase 1.** Only `type:` is implemented. Future fields
(`status:`, `author:`, ...) extend the same syntax without a breaking
change. The contract is "field+value pairs," not "exactly the `type` field."

**Validation.**

- Unknown field name → 400 with `problem.detail` naming the field.
- Malformed shape (no colon, multiple colons, empty field, empty value) →
  400.
- Unknown value for a known field (e.g. `type:nonexistent`) → 400.
  Alternative is to treat as "empty result set"; the issue picks 400 and
  this design follows it. (See [OQ2](#open-questions).)

### Sort semantics

**Enum.** Phase 1 defines exactly four values:

- `updated_desc` *(matches today's behavior — see [OQ3](#open-questions))*
- `updated_asc`
- `id_desc`
- `id_asc`

A missing `sort=` parameter falls back to the default; this is a no-op,
preserving backward compatibility.

**Future extension.** Adding `created_desc`, `title_asc`, etc. is purely
additive; the enum grows. No breaking-change pressure.

### Cursor encoding under variable sort

This is the load-bearing detail. Today the cursor is the opaque base64 of
`(created_at, id)`. If `sort=updated_asc` were honored but the cursor still
keyed on `created_at`, page-2 of a "by-updated-asc" view would jump around
inside the result set arbitrarily.

The cursor envelope therefore needs to include the active sort key so the
store layer can:

1. Validate that the cursor's sort matches the request's sort (mismatch →
   400, not silent re-sort).
2. Apply the correct keyset where-clause (`updated_at` vs `created_at` vs `id`).
3. Round-trip into the next-page `Link` header preserving both.

**Proposed envelope** (post-base64 it stays opaque to the client):

```json
{"v":1,"s":"updated_desc","k":["2026-05-09T12:34:56Z","RFC-0001"]}
```

- `v` schema version (`1`).
- `s` sort key — must match the request's `sort=` exactly; mismatch → 400.
- `k` the tuple of sort-column + tiebreaker-id values from the last row on
  the previous page.

Old cursors minted before this lands have no `v`/`s`/`k` — they survive as
`(created_at, id)` under `sort=updated_desc` by detecting the legacy shape
during decode. (See [OQ4](#open-questions).)

### Total-count headers

Issue #28's Downstream Consumer section asks for "(N of M shown)" — N is
the filtered total, M is the unfiltered total. The existing
`X-Total-Count` header is the *filtered* total (matches today's semantics
where unfiltered = total).

**Proposal:** add a second header `X-Total-Count-Unfiltered` only when at
least one `filter=` is present. When no filter is active, the two values
would be identical and the existing single header suffices.

The header is purely informational; it does not affect pagination math.
Computing it adds one extra `COUNT(*)` per request — acceptable for the
directory page's traffic profile, but worth pinning. (See [OQ5](#open-questions).)

### Error contract

All validation failures return `application/problem+json` (RFC 7807) with
`status: 400` and a `type` discriminator. Field-level detail in
`problem.detail`:

- `urn:rfc-api:problem:bad-request` with `detail: "unknown filter field: foo"`
- `urn:rfc-api:problem:bad-request` with `detail: "unknown type: nonexistent"`
- `urn:rfc-api:problem:bad-request` with `detail: "sort value out of range: weird_order"`
- `urn:rfc-api:problem:bad-request` with `detail: "cursor sort mismatch: cursor=updated_desc, request=id_asc"`

This matches the existing `httperr.classify` seam — a new
`domain.ErrBadFilter` sentinel maps to 400, same as the existing
`domain.ErrBadCursor`.

## API / Interface Changes

**OpenAPI parameter additions** (under `components.parameters`):

```yaml
ListDocsFilter:
  name: filter
  in: query
  required: false
  description: |
    Repeatable. Each value is `field:value`. Within a field the semantics
    are OR; across fields the semantics are AND. Phase 1 supports
    `type:<DocumentType id>`. Unknown fields or values return 400.
  schema:
    type: array
    items:
      type: string
      pattern: '^[a-z][a-z0-9_]*:[a-zA-Z0-9_-]+$'
  style: form
  explode: true

ListDocsSort:
  name: sort
  in: query
  required: false
  description: |
    Single value, fixed enum. Default is `updated_desc`. Adding a sort
    invalidates cursors that were minted under a different sort — the
    server returns 400 on mismatch rather than silently re-sorting.
  schema:
    type: string
    enum: [updated_desc, updated_asc, id_desc, id_asc]
    default: updated_desc
```

**listDocs parameter list** grows:

```yaml
/api/v1/docs:
  get:
    operationId: listDocs
    parameters:
      - $ref: "#/components/parameters/Limit"
      - $ref: "#/components/parameters/Cursor"
      - $ref: "#/components/parameters/ListDocsFilter"
      - $ref: "#/components/parameters/ListDocsSort"
```

**Headers.** Response gains `X-Total-Count-Unfiltered` *only* when at least
one filter is active.

**Sentinels.** New `domain.ErrBadFilter` and `domain.ErrCursorSortMismatch`
errors; corresponding cases in `httperr.classify`.

## Data Model

No schema changes. The `documents` table already has the columns required
for every Phase 1 sort key (`updated_at`, `id`, `created_at`). Future
sort keys may need new indexes; out of scope.

**Indexes worth checking before merge** (probably already exist, verify):

- `documents(updated_at DESC, id ASC)` — for `sort=updated_desc` keyset.
- `documents(updated_at ASC, id ASC)` — for `sort=updated_asc` keyset.
- `documents(id DESC)`, `documents(id ASC)` — id-sort variants.

If any are missing, the consuming IMPL doc adds a migration.

## Testing Strategy

**Contract test** (`test/contract/`):

- listDocs round-trip: filter-only → result is a subset of unfiltered.
- listDocs round-trip: sort-only → result has the same set, order
  changed.
- listDocs round-trip: filter + sort + cursor across two pages — page 2
  stays inside the filtered/sorted view.
- Invalid `filter=` shape → 400 problem+json with the expected `detail`.
- Invalid `sort=` value → 400.
- Cursor sort mismatch → 400.

**Unit tests:**

- Cursor encoder: round-trip every sort variant.
- Filter parser: every malformed-shape case (no colon, multiple colons,
  empty field, empty value, unknown field).
- Store layer: keyset queries for every (sort, with/without filter) pair.

**Integration tests** (`test/integration/postgres/`):

- Live Postgres: filter+sort+cursor traversal across 30+ rows hitting
  multiple types confirms cursor stability under concurrent ingest is
  preserved (existing IMPL-0002 invariant).

**Regression guards:**

- Existing `listDocs` callers (no `filter=`, no `sort=`) keep their exact
  response — same set, same order, same headers (no
  `X-Total-Count-Unfiltered`).

## Migration / Rollout Plan

Purely additive — no callers break. rfc-site picks up the change via
`just gen-api` after rfc-api ships, and Phase 7b unblocks.

Suggested ordering (will be pinned in the consuming IMPL doc):

1. OpenAPI change first (so rfc-site can codegen against the new types
   while the implementation lands).
2. Handler + store + cursor envelope.
3. Contract test.
4. Tag a release. rfc-site bumps the OpenAPI pin, regenerates, lands
   Phase 7b.

No reindex or migration is required — this is an HTTP-surface change
only, not a data-model change.

## Open Questions

### OQ1: Within-field filter semantics — OR or AND?

**a)** **OR within field** (e.g. `filter=type:rfc&filter=type:adr` → RFC ∪ ADR).
Matches the obvious UI metaphor (multi-select), matches REST idioms,
matches what rfc-site's `<DirectoryToolbar>` will produce. *Recommended.*

**b)** **AND within field** (intersect) — nonsensical for `type:` since a
single document has exactly one type. Would only matter for set-valued
fields like `author:` in the future, and even there OR is the obvious
default for a "show me anything by Alice or Bob" UI.

**c)** **Field-specific** — declare it per field. More flexible but more
surface to specify and document. Likely YAGNI.

### OQ2: Unknown filter value — 400 or empty result?

**a)** **400** with `problem.detail` naming the unknown value. Issue #28
picked this. Aligns with how `searchDocs` currently treats unknown
`?type=`. Surfaces typos loudly.

**b)** **Empty result, 200** — treats `filter=type:zzz` like a perfectly
valid query that happens to match nothing. Friendlier to clients that
echo URL params from user input, but masks bugs.

### OQ3: Default sort — `updated_desc` or `created_desc`?

**a)** **`updated_desc`** — matches what a "what's been touched recently"
directory view wants. *But* this is **not** today's behavior — today's
default is `created_at DESC`. Picking this *as the new default* shifts
existing-caller output silently.

**b)** **`created_desc`** — preserves today's behavior exactly; rfc-site
explicitly opts into `?sort=updated_desc`. No silent change for existing
callers.

**c)** Document both as values, make `updated_desc` the documented default
*and* flip the store-layer hardcoded order to match. Acceptable as a
visible (but minor) behavior shift on a `minor` release with the
behavior-change call-out in the changelog.

### OQ4: Cursor compatibility across this change

**a)** **Versioned envelope** (`{"v":1,"s":…,"k":[…]}`). Old cursors lack
`v`/`s` and decode as legacy `(created_at, id)` under `sort=updated_desc`.
Clean. *Recommended.*

**b)** **Hard break** — every cursor minted before this lands becomes
invalid. Acceptable only if we know there are no long-lived bookmarked
cursors in the wild. Today there aren't, but I'd rather not bake the
assumption in.

**c)** **Sort-agnostic cursor** — encode every sortable column in the
cursor so a single payload works under any sort. Larger, less clear, and
forces the decoder to know about every future column.

### OQ5: Total-count header shape

**a)** Always return `X-Total-Count` (current behavior, but reflects the
filtered view); add `X-Total-Count-Unfiltered` only when a filter is
active. *Recommended* — minimum visible change for unfiltered callers.

**b)** Always return both headers. Simpler conditional in handlers but
costs a redundant COUNT(*) on every cross-type list request.

**c)** Roll both into a single composite header — overengineered.

### OQ6: Validation strictness for empty values

**a)** Reject `filter=type:` (trailing colon, empty value) with 400.
*Recommended.* Matches the strict-input posture of the rest of the API.

**b)** Treat empty value as "any" — equivalent to omitting the filter for
that field. Friendlier but adds a special case to the parser.

### OQ7: Does `listDocsByType` get deprecated?

**a)** **No.** Path-scoped per-type list is the natural REST shape for
"all docs of one type"; `filter=type:X` is the natural shape for "docs
across N selected types". They coexist; rfc-site picks per call site.
*Recommended.*

**b)** **Yes, soft-deprecate.** Mark `listDocsByType` deprecated in OpenAPI
and have rfc-site migrate to `?filter=type:X`. Reduces duplication but
adds noise to the directory page's loader for the common single-type case.

### OQ8: Should `?sort=` also apply to `listDocsByType`?

**a)** **Yes** — sort is an orthogonal concern; both endpoints benefit.
Slight extra implementation cost, but the cursor envelope already has
to handle sort across `listDocs`, so reusing it here is cheap.

**b)** **No, separate concern.** Keep `listDocsByType` minimal. Defer
until a concrete UI need shows up. *Recommended* — YAGNI.

## References

- [Issue #28](https://github.com/donaldgifford/rfc-api/issues/28) — the
  contract change request, with rfc-site's Phase 7b context.
- [rfc-site IMPL-0004 Phase 7b](https://github.com/donaldgifford/rfc-site/blob/main/docs/impl/0004-build-rfc-portal-components-per-inv-0002-inventory.md#L385)
  — the downstream `<DirectoryToolbar>` consumer.
- [DESIGN-0002 #Cross-type concerns][design-0002] — the prior shape of
  `/api/v1/docs`; this design tightens the `?type=` loose end into a full
  filter/sort contract.
- [RFC 8288](https://datatracker.ietf.org/doc/html/rfc8288) Web Linking
  — `Link: rel=next` / `rel=prev` shape for paginated responses.
- [RFC 7807](https://datatracker.ietf.org/doc/html/rfc7807)
  Problem Details for HTTP APIs — the existing error envelope this design
  reuses.
- `internal/store/postgres/docs.go` — current store implementation with
  hardcoded `ORDER BY created_at DESC, id ASC`.
- `internal/server/cursor` — cursor encode/decode seam that grows the
  versioned envelope.
