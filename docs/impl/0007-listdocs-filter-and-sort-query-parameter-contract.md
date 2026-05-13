---
id: IMPL-0007
title: "listDocs filter and sort query parameter contract"
status: Draft
author: Donald Gifford
created: 2026-05-13
---
<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0007: listDocs filter and sort query parameter contract

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-05-13

<!--toc:start-->
- [Objective](#objective)
- [Scope](#scope)
  - [In Scope](#in-scope)
  - [Out of Scope](#out-of-scope)
- [Implementation Phases](#implementation-phases)
  - [Phase 1: Parser primitives + cursor envelope](#phase-1-parser-primitives--cursor-envelope)
  - [Phase 2: Store-layer parameterized sort + filter](#phase-2-store-layer-parameterized-sort--filter)
  - [Phase 3: Handler wiring + Link-header preservation](#phase-3-handler-wiring--link-header-preservation)
  - [Phase 4: OpenAPI + contract test](#phase-4-openapi--contract-test)
  - [Phase 5: Rollout coordination](#phase-5-rollout-coordination)
- [File Changes](#file-changes)
- [Testing Plan](#testing-plan)
- [Dependencies](#dependencies)
- [Open Questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Objective

Implement the listDocs filter and sort query parameter contract defined in
DESIGN-0003, so the rfc-site Directory Toolbar can mirror multi-select type
filters + a sort dropdown to the URL and have server-side pagination remain
coherent across that filtered/sorted view. The change is purely additive:
callers passing neither `?filter=` nor `?sort=` see identical behavior to
today (unfiltered, `created_at DESC, id ASC` keyset).

**Implements:** [DESIGN-0003][design-0003] (resolves all 8 OQs); unblocks
[issue #28](https://github.com/donaldgifford/rfc-api/issues/28) and
downstream [rfc-site IMPL-0004 §Phase 7b][rfc-site-impl-0004].

[design-0003]: ../design/0003-listdocs-filter-and-sort-query-parameter-contract.md
[rfc-site-impl-0004]: https://github.com/donaldgifford/rfc-site/blob/main/docs/impl/0004-build-rfc-portal-components-per-inv-0002-inventory.md#L385

## Scope

### In Scope

- Parse `?filter=field:value` (repeatable; Phase 1 supports `type:` only) and `?sort=<enum>` on `/api/v1/docs`.
- Cursor envelope versioning: `{"v":1,"s":"<sort>","k":[<sort-col-val>,<id>]}`. Legacy cursors decode under the new default sort.
- Parameterize `internal/store/postgres/docs.Docs.List` ORDER BY across the six enum values; respect `type:` filter list.
- Update OpenAPI (`api/openapi.yaml`) with `ListDocsFilter` + `ListDocsSort` parameters; keep contract test green.
- Preserve `filter=` + `sort=` across `Link: rel=next` / `rel=prev` pagination links.
- Conditional `X-Total-Count-Unfiltered` header when at least one `filter=` is active.
- Contract + integration + unit tests for every (sort, with/without filter, with/without cursor) permutation.
- CHANGELOG-equivalent release-note wording for the PR body.

### Out of Scope

- Filter fields other than `type:`. The parser is open-ended (it tolerates unknown fields by emitting 400), but no other field gets a validator in Phase 1.
- Modifying `listDocsByType` (`/api/v1/{type}/`). Path-scoped per-type lists keep their current minimal contract per DESIGN-0003 OQ7/OQ8.
- Modifying `searchDocs`. It already has `?type=`; bolting `filter=` / `sort=` onto Meili-backed search is a separate conversation.
- Index migrations. Phase 2 verifies that the keysets are fast enough on the current schema; if a missing index forces an explicit migration, that's a Phase 2 add — but the working assumption is no migration needed.
- The rfc-site Phase 7b implementation itself. That's tracked separately on rfc-site's repo.

## Implementation Phases

Each phase builds on the previous one. A phase is complete when all its tasks
are checked off and its success criteria are met.

---

### Phase 1: Parser primitives + cursor envelope

Stand up the small, pure pieces the handler and store both depend on: the
filter parser, the sort enum, and the versioned cursor envelope. Nothing
in this phase is HTTP-aware; everything lives below the handler edge so
unit tests can drive it without spinning a server.

#### Tasks

- [ ] **Filter parser.** Add an unexported `parseFilters([]string) ([]Filter, error)` in `internal/server/handler/listquery.go` (or a new `internal/server/listquery/` package — see [OQ1](#open-questions)). Validates the `field:value` shape; rejects empty field, empty value, missing colon, multiple colons. Returns a typed slice the store can consume. Wraps malformed input with a package-local `ErrBadFilter` sentinel.
- [ ] **Sort enum.** Add a `Sort` type (string newtype) and `ParseSort(string) (Sort, error)` that accepts the six values from DESIGN-0003 #Sort-semantics. Empty input returns the default `SortCreatedDesc`. Unknown values wrap a package-local `ErrBadSort` sentinel.
- [ ] **Cursor envelope upgrade.** In `internal/server/cursor`, add a versioned encode path emitting `{"v":1,"s":<sort>,"k":[<value>,<id>]}`. The legacy decode path (no `v` / `s` / `k` field present) survives as `(created_at, id)` under `SortCreatedDesc`. Add a `Cursor.Sort()` accessor so the handler can detect cursor-sort mismatch.
- [ ] **Cursor-sort mismatch error.** When the cursor decodes to one sort and the request asks for a different one, encode/decode helpers return a wrapped `ErrCursorSortMismatch` sentinel. The handler edge wraps with `domain.ErrInvalidInput` so `httperr.classify` maps it to 400 — no new domain sentinel required (see DESIGN-0003 #Error-contract and [OQ2](#open-questions)).
- [ ] **Unit tests for the filter parser.** Cover: every malformed shape (no colon, multi-colon, empty field, empty value, leading/trailing whitespace), the happy path (single + repeated values, distinct fields). Table-driven, parallel.
- [ ] **Unit tests for the sort enum.** Each of the six values round-trips. Empty input returns the default. Unknown value returns `ErrBadSort` + message.
- [ ] **Unit tests for the cursor envelope.** Round-trip every sort variant. Legacy decode (a cursor minted by today's encoder) returns the right `(time, id)` tuple under `SortCreatedDesc`. Mismatch on decode returns `ErrCursorSortMismatch`.
- [ ] Run `make lint` and `make fmt`; fix any warnings.

#### Success Criteria

- `go test ./internal/server/cursor/... ./internal/server/handler/...` passes (or the new `internal/server/listquery/...` package if that path is chosen).
- Cursor envelope is backward compatible: a token minted by today's encoder decodes cleanly under the new decoder without throwing.
- `make lint` clean.
- No public API surface changes outside the new types — `Filter`, `Sort`, `Cursor` exported types are the only additions.
- No store-layer or handler-layer code has been touched yet; this phase only adds the primitives.

---

### Phase 2: Store-layer parameterized sort + filter

Replace the five hardcoded `ORDER BY created_at DESC, id ASC` clauses in
`internal/store/postgres/docs.go` with a dispatch on the active `Sort`,
and add a `[]string` type filter to the WHERE clause path. The store
API gains optional fields on the existing `List` call; in-process and
in-memory store implementations (the `internal/store/memory` test
double) implement the same surface.

#### Tasks

- [ ] **Extend the store interface.** `store.Docs.List` (or whatever the current method is named) grows a `ListOptions` struct argument carrying `Sort`, `TypeIDs []string`, `Limit`, `Cursor`. Single-struct args fix the gocritic `hugeParam` rule at 80 bytes — verify the resulting struct stays under that or take it by pointer (see [OQ3](#open-questions)).
- [ ] **Postgres `List` implementation.** Parameterize the SQL: pick the ORDER BY based on `Sort`; add `AND type = ANY($N::text[])` to the WHERE when `TypeIDs` is non-empty; pick the keyset comparison based on the active sort key. Five paths consolidate to one templated builder, or stay as six SQL constants if that reads cleaner (see [OQ4](#open-questions)).
- [ ] **Index inventory.** Verify `(updated_at DESC, id ASC)` and `(updated_at ASC, id ASC)` and `(id ASC)` / `(id DESC)` indexes already exist via `\d documents` against a fresh compose-up Postgres. If any are missing, add a forward-only migration under `db/migrations/`. The working assumption is the `(created_at DESC, id ASC)` from IMPL-0002 is the only one currently present, so 2–3 migrations may be needed.
- [ ] **Memory store implementation.** Update `internal/store/memory` (test-only fake) to match the new interface. Sort + filter applied in Go, not SQL. Keeps the handler and contract test suites driveable without a Postgres dependency.
- [ ] **Unfiltered-count helper.** Add a `Docs.CountAll(ctx)` (or extend the existing total-count seam) so the handler can compute the `X-Total-Count-Unfiltered` value when at least one filter is active. Implementation: single `SELECT count(*) FROM documents` (see [OQ5](#open-questions) for the alternative of "count via the same query path with no filter").
- [ ] **Unit tests for the memory store.** Cover every (sort × filter × cursor) combination. Distinct sets for distinct sorts; filter-on subset; cursor advances correctly across each sort.
- [ ] **Integration tests for the Postgres store.** Build-tagged `integration` tests under `internal/store/postgres/*_test.go` that seed 30+ rows across at least 3 types and traverse pages under each sort+filter combination. Assert cursor stability under concurrent ingest is preserved (existing IMPL-0002 invariant) — add a goroutine that inserts during traversal and assert no duplicates / no skips in the result stream.
- [ ] Run `make lint`, `make fmt`, `make test-integration`.

#### Success Criteria

- `go test ./internal/store/memory/... -race` and `make test-integration` both green.
- Every (sort, with-filter, without-filter) combination produces a consistent ordered set across two pages.
- Cursor under a given sort cannot be honored under a different sort — store returns `ErrCursorSortMismatch` upward.
- No regression on existing callers: `List(ctx, ListOptions{})` returns the same set in the same order as the current `List` call.
- `EXPLAIN ANALYZE` on each (sort × filter) keyset query uses an index scan, not a sequential scan, against a 1K+ row dev dataset (or migration added).

---

### Phase 3: Handler wiring + Link-header preservation

Connect the handler to the new primitives. Parse query params, validate
against the live type registry, call the store, build response headers
with filter+sort preserved in the `rel=next` / `rel=prev` URLs.

#### Tasks

- [ ] **Parse query in `internal/server/handler/docs.go`.** Read `r.URL.Query()` for `filter[]` (repeating) + `sort` + existing `limit` / `cursor`. Call the Phase 1 parsers; wrap all `ErrBad*` with `domain.ErrInvalidInput` at the handler edge.
- [ ] **Validate filter values against the type registry.** For `filter=type:<id>`, look up the type id against `domain.Registry` (already threaded through the handler dep struct); reject unknown ids with `domain.ErrInvalidInput` + `detail: "unknown type: <id>"`.
- [ ] **Cursor-sort cross-check.** If both `?cursor=` and `?sort=` are present and the cursor's sort doesn't match the request's sort, return 400 with `detail: "cursor sort mismatch: cursor=<a>, request=<b>"`. Sourced from Phase 1's `ErrCursorSortMismatch`.
- [ ] **Call the store with `ListOptions`.** Pass through the parsed `Sort`, `TypeIDs`, `Limit`, `Cursor`. Existing dependency injection of `store.Docs` is reused; no new constructor wiring needed.
- [ ] **`X-Total-Count` header.** Set to the filtered total (today's semantics — current behavior preserved when no filter is active).
- [ ] **`X-Total-Count-Unfiltered` header.** Emit only when at least one `filter=` is active. Value comes from the Phase 2 `CountAll` helper. When no filter is active, header is omitted entirely (zero visible change for unfiltered callers).
- [ ] **Link-header preservation.** Update the `rel=next` / `rel=prev` URL builder to include every active `filter=` value (repeated) + `sort=` (single). The next-page cursor is minted with the *same* sort the request used, so cursor + sort stay aligned across page traversal.
- [ ] **Handler unit tests.** Add cases to the existing `internal/server/handler/docs_test.go` covering: filter-only happy path, sort-only happy path, filter+sort+cursor round-trip, every malformed-input → 400 case, `X-Total-Count-Unfiltered` present/absent based on filter, Link headers carry the params verbatim.
- [ ] Run `make lint`, `make fmt`.

#### Success Criteria

- `go test ./internal/server/...` green (race detector on).
- Every malformed input returns 400 + `application/problem+json` with a `detail` that names the specific failure (matches DESIGN-0003 #Error-contract).
- Link headers round-trip filter+sort cleanly: pasting the `rel=next` URL into a fresh request yields the next page within the same filtered/sorted view.
- An unfiltered request (no `filter=`) returns the same response bytes as today's call to the existing handler — verified by adding a "no params, identical to baseline" assertion in the handler test.

---

### Phase 4: OpenAPI + contract test

Surface the new parameters in the spec and pin the contract with a test
in `test/contract/` (single home for cross-cutting contract tests per
the IMPL-0006 convention).

#### Tasks

- [ ] **Add `ListDocsFilter` parameter to `api/openapi.yaml`.** Shape:
  ```yaml
  ListDocsFilter:
    name: filter
    in: query
    required: false
    description: |
      Repeatable. Each value is `field:value`. OR within a field; AND
      across fields. Phase 1 supports `type:<DocumentType id>`. See
      DESIGN-0003.
    schema:
      type: array
      items:
        type: string
        pattern: '^[a-z][a-z0-9_]*:[a-zA-Z0-9_-]+$'
    style: form
    explode: true
  ```
- [ ] **Add `ListDocsSort` parameter.**
  ```yaml
  ListDocsSort:
    name: sort
    in: query
    required: false
    description: |
      Single value, fixed enum. Default is `created_desc` (today's
      behavior). See DESIGN-0003 #OQ3.
    schema:
      type: string
      enum: [created_desc, created_asc, updated_desc, updated_asc, id_desc, id_asc]
      default: created_desc
  ```
- [ ] **Reference both from `listDocs`.** Extend the `parameters` list with `$ref` lines under `/api/v1/docs.get`.
- [ ] **Document `X-Total-Count-Unfiltered` in response headers.** The spec currently declares `X-Total-Count`; add the conditional header under the same response.
- [ ] **Add `test/contract/listdocs_filter_sort_test.go`.** Drive the in-process handler through `kin-openapi` request/response validation per the existing `contract_test.go` pattern. Assertions:
  - Filter-only response is a strict subset of the unfiltered baseline.
  - Sort-only response has the same multiset, order changed.
  - Filter + sort + cursor: page 1 + page 2 stay inside the filtered+sorted view; cursor on page 2 still encodes the same sort.
  - `filter=foo:bar` (unknown field) → 400 problem+json with the expected `detail`.
  - `filter=type:nonexistent` → 400.
  - `sort=weird` → 400.
  - Cursor minted under `created_desc` + request with `sort=id_asc` → 400 cursor mismatch.
- [ ] **Existing contract tests stay green.** Run `go test ./test/contract/...` — additive change should not break any prior assertion.
- [ ] Run `make lint`, `make fmt`.

#### Success Criteria

- `make test` and `go test ./test/contract/...` green.
- `api/openapi.yaml` validates under `kin-openapi` (the existing in-process validator catches OAS 3.1 violations — see CLAUDE.md #pitfalls).
- Every error case is asserted with both the status code (400) and the `Content-Type: application/problem+json` envelope.
- The contract test file's failure messages name the specific divergence (subset violation / order violation / missing param round-trip / wrong status) so a future regression is debuggable from the test log alone.

---

### Phase 5: Rollout coordination

User-driven; this phase records the operational steps that close the
loop with rfc-site.

#### Tasks

- [ ] **(user)** Open the PR; paste the operator runbook block below into the description.
- [ ] **(user)** Tag a minor-version release (`make release TAG=v0.2.0` or whatever the next is per the semver-minor label).
- [ ] **(user)** Open a follow-up issue on rfc-site to bump the OpenAPI pin (`just gen-api`) and implement Phase 7b's `<DirectoryToolbar>` against the new contract.
- [ ] **(user)** Close issue #28 with a link to the merged PR.

#### Operator runbook (paste into PR body / release notes)

> **`listDocs` gains `?filter=` + `?sort=` query parameters.**
>
> - `?filter=type:rfc&filter=type:adr` — repeatable; OR within field, AND across fields. Phase 1 supports only `type:`.
> - `?sort=` — one of `created_desc` (default), `created_asc`, `updated_desc`, `updated_asc`, `id_desc`, `id_asc`. Missing param = today's behavior.
> - `Link: rel=next` / `rel=prev` preserve both params so cursor traversal stays inside the filtered/sorted view.
> - Response headers: `X-Total-Count` is the filtered total; `X-Total-Count-Unfiltered` is emitted only when a filter is active.
> - 400 + `application/problem+json` on every invalid value, including cursor-sort mismatch.
>
> **Migration:** none. Purely additive. Old cursors keep working. No DB
> migration required (Phase 2 verified existing indexes are sufficient; if a
> migration was added there, call it out here).
>
> **Downstream:** rfc-site Phase 7b unblocks once it bumps the OpenAPI pin.

#### Success Criteria

- PR merged + release tagged.
- rfc-site coordination issue filed; rfc-site can re-run `just gen-api` against the new OpenAPI.
- Issue #28 closed.

---

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/server/handler/listquery.go` (or `internal/server/listquery/`) | Create | Filter parser + sort enum + their unit tests. See OQ1. |
| `internal/server/cursor/cursor.go` | Modify | Versioned envelope (`{"v":1,"s":…,"k":[…]}`); legacy decode path; `Sort()` accessor. |
| `internal/server/cursor/cursor_test.go` | Modify | Round-trip every sort variant; legacy decode; mismatch detection. |
| `internal/server/handler/docs.go` | Modify | Parse + validate filter/sort; cursor-sort cross-check; Link-header preservation; conditional `X-Total-Count-Unfiltered`. |
| `internal/server/handler/docs_test.go` | Modify | New cases for filter/sort happy paths + every malformed input. |
| `internal/store/store.go` | Modify | Extend `Docs.List` to take `ListOptions`. |
| `internal/store/postgres/docs.go` | Modify | Parameterize ORDER BY + WHERE on `Sort` + `TypeIDs`; six (sort × filter) keyset queries. |
| `internal/store/postgres/docs_test.go` | Modify | Integration tests across every (sort × filter × cursor) combination; concurrent-ingest stability. |
| `internal/store/memory/docs.go` | Modify | Mirror the new `List` interface for test doubles. |
| `db/migrations/000NN_*.sql` | Create (conditional) | Indexes for `updated_at` and `id` keysets if missing. |
| `api/openapi.yaml` | Modify | New `ListDocsFilter` + `ListDocsSort` parameters; reference from `listDocs`; document `X-Total-Count-Unfiltered`. |
| `test/contract/listdocs_filter_sort_test.go` | Create | Contract test covering filter/sort/cursor round-trips + error envelopes. |
| `CLAUDE.md` | Modify | After Phase 5 lands: capture the new endpoint shape + any pitfalls (cursor-sort mismatch handling, conditional header). |

## Testing Plan

- **Unit tests** for every new pure primitive (filter parser, sort enum, cursor envelope) — table-driven, parallel, covering happy path + every documented failure mode.
- **Integration tests** under `internal/store/postgres/` for the new (sort × filter × cursor) permutations; gated `//go:build integration` and driven by `DATABASE_URL`. Must include a concurrent-ingest stability assertion.
- **Contract test** under `test/contract/` for the full HTTP surface, driven through `kin-openapi` against the live `api/openapi.yaml`.
- **Existing tests stay green** — additive change should not require updating any prior assertion. If any do shift (which would indicate an accidental behavior change for unfiltered callers), pause and reconcile rather than rubber-stamping the diff.
- **Coverage target:** every new package ≥80%; the existing handler package coverage does not regress.

## Dependencies

### Upstream

- [DESIGN-0003][design-0003] — accepted design; provides the contract shape, error envelope, cursor envelope schema, header semantics, and all eight resolved OQs.
- IMPL-0002 — Postgres store + cursor pagination; this IMPL extends those seams.
- DESIGN-0001 — error envelope (RFC 7807); reused unchanged.

### Downstream / consumer

- [rfc-site IMPL-0004 §Phase 7b][rfc-site-impl-0004] — the `<DirectoryToolbar>` work that consumes this contract. Phase 5 of this IMPL opens a coordinating issue; the actual implementation lives on the rfc-site repo.

### Blocking work

- None. This work can land independently of any in-flight rfc-api change.

## Open Questions

### OQ1: Where do the filter parser + sort enum live?

**a)** **`internal/server/handler/listquery.go`** — sits next to the
handler that consumes it; matches the codebase's pattern of small
unexported helpers inside `handler/` (cf. the existing `parseListParams`
helper in `docs.go`). Cheap, no new package, low friction.

**b)** **New `internal/server/listquery/` package** — gives the
filter/sort types a clean import path for the contract test to reach
without depending on `handler/` package internals. Mirrors what
IMPL-0006 did with `internal/slug/` (factored out so `test/contract/`
could import). Heavier; a whole package for ~150 lines of code.

**c)** **`internal/domain/`** — promote `Filter` and `Sort` to first-class
domain types alongside `Document` and `DocumentType`. Best for reuse if
search/index/worker ever grow filtering, but probably YAGNI for Phase 1
where only `listDocs` uses them.

*My lean: (a)* — the types are HTTP-shaped (parsed from query strings,
validated against URL conventions), so they belong on the handler side.
The contract test imports the handler's BuildMainHandler already and
asserts behavior at the HTTP layer, not at the type-construction layer,
so the package-internal types don't need to be exported.

### OQ2: New domain sentinel or reuse `ErrInvalidInput`?

DESIGN-0003 floated `domain.ErrBadFilter` + `domain.ErrCursorSortMismatch`
as new sentinels. Inspecting the codebase, `domain.ErrInvalidInput` is
the catch-all for 400-class errors and already covers "bad cursor",
"out-of-range limit", "unknown type id" per its docstring.

**a)** **Reuse `domain.ErrInvalidInput`** with package-local
`listquery.ErrBadFilter` + `listquery.ErrBadSort` +
`listquery.ErrCursorSortMismatch` sentinels for in-package classification.
Wrap with `domain.ErrInvalidInput` at the handler edge. Matches the
existing cursor package pattern (`cursor.ErrInvalid` → wrapped with
`domain.ErrInvalidInput`). No `httperr.classify` change required.

**b)** **Add new domain sentinels** — `domain.ErrBadFilter`,
`domain.ErrCursorSortMismatch`. Requires `httperr.classify` cases too.
Surfaces the failure mode more precisely in domain code but doesn't
materially help the client (the response is still 400 problem+json with
the wrapped detail message).

*My lean: (a)* — the existing one-sentinel-per-class pattern is
deliberate (per `internal/domain/errors.go` comment: "broad-grained" and
"failure modes one callers need to branch on"). Filter parse failures
and sort mismatches aren't a class of failure callers need to branch on
distinctly from "bad cursor" or "unknown type" — they all flow through
the same 400 + problem+json envelope.

### OQ3: Store `List` argument shape

**a)** **Single `ListOptions` struct** with `Sort`, `TypeIDs`, `Limit`,
`Cursor`. Passed by value or pointer based on the gocritic `hugeParam`
threshold (80 bytes — verify post-implementation). The struct grows
additively as new filter fields are added.

**b)** **Variadic functional options** —
`store.Docs.List(ctx, WithSort(s), WithTypes(t), WithLimit(n), WithCursor(c))`.
More idiomatic-Go-modern but adds noise to call sites that pass nothing,
and the codebase doesn't already use this pattern anywhere.

**c)** **Positional args** —
`store.Docs.List(ctx, sort, typeIDs, limit, cursor)`. Simple, but every
new filter is a breaking signature change.

*My lean: (a)* — matches the codebase's existing struct-arg pattern
(e.g. `server.New(*Deps)`, `middleware.CORS(*CORSConfig)`).

### OQ4: Postgres query construction — templated builder or six SQL constants?

**a)** **Single templated query builder** that picks the ORDER BY +
keyset comparison based on `Sort`. ~50 lines of dispatch logic; one place
to update when a new sort key is added.

**b)** **Six (sort) × two (filter present / absent) = 12 SQL constants**,
selected with a switch. More repetition, but every variant is a literal
SQL string you can paste into `psql` to debug, which is friendlier when
something misbehaves at 3am.

**c)** **A small DSL** (e.g. squirrel, goqu) — full abstraction. Not in
the codebase today; introducing it for this is too much.

*My lean: (b)* — six constants are within tolerable repetition, the
codebase prefers literal SQL (`internal/store/postgres/docs.go` already
inlines its query strings), and "paste into `psql`" is genuinely valuable
when debugging keyset edge cases.

### OQ5: How is `X-Total-Count-Unfiltered` computed?

**a)** **Dedicated `Docs.CountAll(ctx)` helper.** Single
`SELECT count(*) FROM documents`. Cheap; one extra query per filtered
request; trivial to cache later if it matters.

**b)** **Reuse the existing total-count seam.** The current handler
issues a count query for `X-Total-Count`; conditionally issue a second
copy with the type filter stripped. Slightly more elegant but tightly
couples the unfiltered count to the filtered-count code path.

**c)** **Skip until needed.** Don't emit the header in Phase 1; defer
to a follow-up IMPL once rfc-site's UI is built and actually consuming
it.

*My lean: (a)* — distinct concern, distinct query, easy to reason about.
Per-request cost is one extra COUNT against an indexed table; not a hot
path.

### OQ6: Concurrent-ingest stability test — where does it live?

**a)** **`internal/store/postgres/docs_test.go`** — extends the existing
IMPL-0002 invariant test. Same `//go:build integration` tag; runs in CI's
existing `integration` job.

**b)** **`test/integration/postgres/`** — server-level integration suite.
More end-to-end (drives the HTTP handler not the store directly), but
slower and farther from the actual seam being tested.

**c)** **Both.** Belt-and-suspenders.

*My lean: (a)* — the invariant is about the keyset's correctness under
concurrent writes; that's a store-layer property, not an HTTP-layer
property. Testing it where the seam lives is more targeted.

### OQ7: Should `listDocsByType` (path-scoped per-type list) also gain `?sort=`?

DESIGN-0003 OQ8 resolved this as "no, YAGNI." Re-asking at the IMPL level
because the cursor-envelope plumbing from Phase 1 makes adding it almost
free — the same `Sort` parameter could flow through `listDocsByType`'s
handler with maybe 10 extra lines.

**a)** **Keep DESIGN-0003 OQ8's resolution — no sort on `listDocsByType`.**
Defer until rfc-site has a concrete UI need.

**b)** **Add it now while the plumbing is fresh.** Modest extra surface;
costs ~30 LoC + a handful of test cases; opens the path for rfc-site to
sort per-type directory views (e.g. `/rfc?sort=updated_desc`) without a
second IMPL doc.

*My lean: (a)* — DESIGN-0003 already pinned this; reopening a decision
during implementation is a small but real coherence cost. Easy to ship
in a 50-line follow-up PR if rfc-site asks for it.

### OQ8: Memory store implementation — full parity or just enough for tests?

The `internal/store/memory` fake powers unit + contract + (some)
integration tests. Postgres is the production store. Phase 2 needs the
memory store updated, but how completely?

**a)** **Full parity.** Memory store implements every (sort × filter ×
cursor) combination correctly. Higher upfront cost; ensures tests
exercise the same surface as production.

**b)** **Minimum viable.** Memory store handles `SortCreatedDesc` only
(today's behavior) plus the type filter; other sorts return
`ErrNotImplemented`. Postgres integration tests cover the rest. Cheaper
in the short term but fragments the test matrix.

*My lean: (a)* — the memory store is the only thing keeping contract
tests Postgres-free in CI. Cutting corners on parity here forces every
new contract assertion to drag a Postgres dependency, which slows the
test loop. The memory store's sort+filter implementation is ~30 LoC of
`sort.Slice` + filter loop — small enough to justify full parity.

## References

- [DESIGN-0003][design-0003] — the contract this IMPL builds.
- [Issue #28](https://github.com/donaldgifford/rfc-api/issues/28) — the
  contract change request.
- [rfc-site IMPL-0004 §Phase 7b][rfc-site-impl-0004] — the downstream
  consumer that unblocks once this ships.
- [DESIGN-0001](../design/0001-rfc-api-http-server-go-nethttp-structure.md)
  — RFC 7807 error envelope and middleware chain this IMPL reuses.
- [DESIGN-0002](../design/0002-documenttype-extensibility-for-multiple-content-types.md)
  — `DocumentType` registry; the source of truth for filter `type:`
  value validation.
- [IMPL-0002](./0002-rfc-api-postgresql-store-implementation.md) — the
  Postgres store + keyset cursor implementation this IMPL extends.
- [IMPL-0006](./0006-sectionslug-consumer-side-slug-contract-implementation.md)
  — recent precedent for the rfc-api ↔ rfc-site contract pattern,
  including the `test/contract/` single-home convention.
- [RFC 7807](https://datatracker.ietf.org/doc/html/rfc7807) Problem
  Details for HTTP APIs.
- [RFC 8288](https://datatracker.ietf.org/doc/html/rfc8288) Web Linking
  / Link header.
