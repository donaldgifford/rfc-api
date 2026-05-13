---
id: IMPL-0007
title: "listDocs filter and sort query parameter contract"
status: In Progress
author: Donald Gifford
created: 2026-05-13
---
<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0007: listDocs filter and sort query parameter contract

**Status:** In Progress
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
- [Resolved Decisions](#resolved-decisions)
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

- [x] **Filter parser.** Add an unexported `parseFilters([]string) ([]filter, error)` in `internal/server/handler/listquery.go` (OQ1=a). Validates the `field:value` shape; rejects empty field, empty value, missing colon, multiple colons. Returns a typed slice the store can consume. Wraps malformed input with a package-local `errBadFilter` sentinel.
- [x] **Sort enum.** Add a `Sort` type (string newtype) and `ParseSort(string) (Sort, error)` that accepts the six values from DESIGN-0003 #Sort-semantics. Empty input returns the default `SortCreatedDesc`. Unknown values wrap `list.ErrInvalidSort`; the handler-edge `parseSort` further wraps with package-local `errBadSort`. Type lives in `internal/store/list/` (the new package OQ3 introduces) since Phase 2 needs the store to dispatch on it; the parser in `listquery.go` is a thin wrapper.
- [x] **Cursor envelope upgrade.** `internal/server/cursor` now emits the v1 envelope `{"v":1,"s":"<sort>","k":["<value>","<id>"]}` on every Encode. Decode sniffs the `v` field and dispatches to a v1 decoder or a legacy `{t,i}` decoder. Legacy cursors return `store.Cursor{Sort: SortCreatedDesc, …}` so callers minted before this commit stay honored. `store.Cursor` grows a `Sort list.Sort` field (zero value treated as `SortCreatedDesc` for backward compat) — the field, not a method, carries the cursor's sort to the handler.
- [x] **Cursor-sort mismatch error.** Deferred to Phase 3 where the handler does the actual cross-check. The cursor package itself only returns `ErrInvalid` for malformed envelopes; the handler-edge `errCursorSortMismatch` sentinel will land alongside the handler wiring in Phase 3 (the file already imports `errors` for the package-local sentinels).
- [x] **Unit tests for the filter parser.** Cover: every malformed shape (no colon, multi-colon, empty field, empty value, leading/trailing whitespace), the happy path (single + repeated values, distinct fields). Table-driven, parallel.
- [x] **Unit tests for the sort enum.** Each of the six values round-trips. Empty input returns the default. Unknown value returns `ErrBadSort` + message. Plus a `TestDefaultSort_PinsCreatedDesc` guard so a future change to the default trips a test instead of silently shifting behavior for unfiltered callers.
- [x] **Unit tests for the cursor envelope.** Six per-sort round-trip cases (`TestRoundTrip_EverySort`), legacy decode (`TestDecode_LegacyEnvelope_AssumesCreatedDesc`), zero-sort defaulting (`TestEncode_ZeroSortDefaultsToCreatedDesc`), unknown-sort rejection, time-sort requires timestamp, plus the existing bad-base64 / too-long / empty cases.
- [x] Run `make lint` and `make fmt`; clean.

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

- [x] **Extend the store interface with variadic functional options** (OQ3=b). New `internal/store/list/` package exports `Option`, `Sort`, `Cursor` (moved from `store` to break the import cycle), `Config`, `Apply`, and `WithSort` / `WithTypes` / `WithLimit` / `WithCursor` constructors. `store.Docs.List(ctx, opts ...list.Option)` replaces today's struct-arg `List(ctx, ListQuery)`; `store.ListQuery` and `store.Cursor` are gone (callers now use `list.Cursor`). Memory + Postgres stores both implement the new shape; service-layer ListByType / ListAll translate (typeID, limit, cursor) into the option set internally.
- [x] **Postgres `List` implementation.** Twelve literal SQL constants (OQ4=b) covering the (6 sorts × 2 filter-present) matrix. Per-sort helper functions select among them based on (hasFilter, hasCursor). Each constant uses the right ORDER BY + keyset comparison + (conditional) `AND type = ANY($N::text[])`. No templated builder; matches the existing literal-SQL style in `docs.go`.
- [ ] **Index inventory.** Verify `(updated_at DESC, id ASC)` and `(updated_at ASC, id ASC)` and `(id ASC)` / `(id DESC)` indexes already exist via `\d documents` against a fresh compose-up Postgres. If any are missing, add a forward-only migration under `db/migrations/`. The working assumption is the `(created_at DESC, id ASC)` from IMPL-0002 is the only one currently present, so 2–3 migrations may be needed.
- [x] **Memory store implementation.** Updated to match the new interface with full sort + filter parity (OQ8=a). Sort dispatch via a `less` comparator and `rowAfterCursor` predicate keyed on `list.Sort`; type filter via `slices.Contains`. ~80 LoC; keeps the handler + contract test suites driveable without a Postgres dependency.
- [x] **Unfiltered-count helper.** Added `Docs.CountAll(ctx)` (OQ5=a) to the store interface, with Postgres + memory implementations. Single `SELECT count(*) FROM documents` on the Postgres side; trivial `len(s.ordered)` on the memory side. Handler calls it only when at least one filter is active (wired in Phase 3).
- [x] **Unit tests for the memory store.** New `TestList_SortVariants` covers every documented sort against the 3-doc seed; `TestList_FilterOR_AcrossMultipleTypes` pins the OR-within-field semantics; `TestList_CursorMatchesSort` verifies NextCursor carries the active sort; `TestList_PaginationUnderSort` exercises cursor traversal under id_asc to drive the non-default sort path; `TestCountAll_UnfilteredTotal` pins CountAll's invariant. The existing `TestList_CrossType`, `TestList_ByType`, `TestList_PaginationCursor`, `TestList_BadLimit` tests stay green under the new option-based API.
- [x] **Integration tests for the Postgres store.** Added `TestDocs_List_SortVariants` (every documented sort against 3 seeded rows with distinct created/updated/id ordering), `TestDocs_List_FilterOR` (multi-value WithTypes returns the OR set), `TestDocs_List_CursorUnderUpdatedSort` (keyset on updated_at — distinct column from default), and `TestDocs_CountAll_Unfiltered`. The existing `TestDocs_List_KeysetPaginationStable` already exercises cursor stability under mid-traversal ingest and stays green under the new option API.
- [x] Run `make lint`, `make fmt`. `make test-integration` deferred (needs a running Postgres on `DATABASE_URL`); CI's `integration` job exercises the live path on every push.

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
- [ ] **Call the store with the functional-option set.** `store.List(ctx, list.WithSort(s), list.WithTypes(t...), list.WithLimit(n), list.WithCursor(c))`. Existing dependency injection of `store.Docs` is reused; no new constructor wiring needed.
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
| `internal/server/handler/listquery.go` | Create | Filter parser + sort enum + their unit tests. Handler-local, unexported types (OQ1=a). |
| `internal/store/list/list.go` | Create | New package: `Option` functional-option type, `Sort` enum, `WithSort` / `WithTypes` / `WithLimit` / `WithCursor` constructors, internal `config` accumulator (OQ3=b). |
| `internal/store/list/list_test.go` | Create | Unit tests for the options package: empty options → defaults, each `With*` mutates only its slot, sort enum round-trips. |
| `internal/server/cursor/cursor.go` | Modify | Versioned envelope (`{"v":1,"s":…,"k":[…]}`); legacy decode path; `Sort()` accessor. |
| `internal/server/cursor/cursor_test.go` | Modify | Round-trip every sort variant; legacy decode; mismatch detection. |
| `internal/server/handler/docs.go` | Modify | Parse + validate filter/sort; cursor-sort cross-check; Link-header preservation; conditional `X-Total-Count-Unfiltered`. |
| `internal/server/handler/docs_test.go` | Modify | New cases for filter/sort happy paths + every malformed input. |
| `internal/store/store.go` | Modify | `Docs.List(ctx, opts ...list.Option)` signature; add `Docs.CountAll(ctx)` (OQ5=a). |
| `internal/store/postgres/docs.go` | Modify | Six literal SQL constants for `(sort × filter-present)` (OQ4=b); `CountAll` implementation. |
| `internal/store/postgres/docs_test.go` | Modify | Integration tests across every (sort × filter × cursor) combination; concurrent-ingest stability (OQ6=a). |
| `internal/store/memory/docs.go` | Modify | Full parity: sort + filter + cursor across every variant (OQ8=a). |
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

## Resolved Decisions

All 8 OQs resolved during the 2026-05-13 review pass. Where the
resolution overrides a stated lean or a codebase convention, the
rationale is captured below so a future reader can trace the call.

### OQ1: Filter parser + sort enum live in **`internal/server/handler/listquery.go`** ✓

Handler-local file, unexported types. Matches the existing pattern of
small helpers inside `handler/` (cf. `parseListParams` in `docs.go`). No
new package, no test-import gymnastics — the contract test asserts at
the HTTP layer through `BuildMainHandler`, not at the type-construction
layer.

### OQ2: Reuse `domain.ErrInvalidInput` with package-local sentinels ✓

`listquery` adds package-local `ErrBadFilter`, `ErrBadSort`,
`ErrCursorSortMismatch` sentinels for in-package classification, then
wraps with `domain.ErrInvalidInput` at the handler edge. This mirrors
the existing `cursor.ErrInvalid` → `domain.ErrInvalidInput` flow.
**No** new domain sentinel and **no** `httperr.classify` case is
added — the existing 400 → problem+json envelope carries the wrapped
detail message and that's enough for the client.

### OQ3: Store `List` takes **variadic functional options** ✓

```go
docs, next, err := store.List(ctx,
    list.WithSort(list.SortUpdatedDesc),
    list.WithTypes("rfc", "adr"),
    list.WithLimit(25),
    list.WithCursor(c),
)
```

**Why this overrides my recommendation (a, struct arg).** The codebase
historically uses struct-arg signatures (`server.New(*Deps)`,
`middleware.CORS(*CORSConfig)`). Donald's explicit guidance during
review: *"we should always move towards go idiomatic code when its time
to, and its time."* Functional options are the more idiomatic Go
pattern for an extensible optional-knob surface; adopting them on a
*new* API surface (rather than churning every existing struct arg) is
the natural pivot point. Don't retrofit existing struct-arg call sites
elsewhere — leave them until they're touched for unrelated reasons.

### OQ4: Six literal SQL constants, not a templated builder ✓

`internal/store/postgres/docs.go` keeps SQL literal. Pick the query
string with a switch on `(Sort, hasFilter)`. Six (sort) × two (filter
present / absent) = 12 constants — paste-into-psql friendly, matches
the existing style, and the repetition is bounded.

### OQ5: Dedicated `Docs.CountAll(ctx)` helper ✓

Single `SELECT count(*) FROM documents`. The handler calls it only when
at least one `filter=` is active, populating `X-Total-Count-Unfiltered`
conditionally. Distinct concern, distinct query — easy to reason about,
easy to cache later if it matters.

### OQ6: Concurrent-ingest stability test lives in **`internal/store/postgres/docs_test.go`** ✓

Same `//go:build integration` tag as the existing IMPL-0002 invariant
tests. Tests the store-layer keyset invariant where the seam lives,
not redundantly at the HTTP layer.

### OQ7: No `?sort=` on `listDocsByType` ✓

Keep DESIGN-0003 OQ8's resolution. The plumbing is cheap to add later
in a 50-line follow-up PR if rfc-site grows a concrete UI need;
reopening the decision now would just churn the design coherence.

### OQ8: Memory store reaches **full parity** with Postgres ✓

`internal/store/memory` implements every (sort × filter × cursor)
combination correctly. Implementation: a slice copy, `sort.Slice`, the
type filter as a `slices.ContainsFunc`, and the cursor as a linear scan
to find the start row. ~50 LoC. Keeps contract + handler tests
Postgres-free in CI.

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
