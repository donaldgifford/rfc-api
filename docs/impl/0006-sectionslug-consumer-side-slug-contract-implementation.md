---
id: IMPL-0006
title: "section_slug consumer-side slug contract implementation"
status: Draft
author: Donald Gifford
created: 2026-05-08
---
<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0006: section_slug consumer-side slug contract implementation

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-05-08

<!--toc:start-->
- [Objective](#objective)
- [Scope](#scope)
  - [In Scope](#in-scope)
  - [Out of Scope](#out-of-scope)
- [Implementation Phases](#implementation-phases)
  - [Phase 1: Algorithm port — pure github-slugger-faithful slug](#phase-1-algorithm-port--pure-github-slugger-faithful-slug)
    - [Tasks](#tasks)
    - [Success Criteria](#success-criteria)
  - [Phase 2: Stateful per-document slugger + indexer wiring](#phase-2-stateful-per-document-slugger--indexer-wiring)
    - [Tasks](#tasks-1)
    - [Success Criteria](#success-criteria-1)
  - [Phase 3: Snapshot-fixture contract test](#phase-3-snapshot-fixture-contract-test)
    - [Tasks](#tasks-2)
    - [Success Criteria](#success-criteria-2)
  - [Phase 4: Migration + rollout coordination](#phase-4-migration--rollout-coordination)
    - [Tasks](#tasks-3)
    - [Success Criteria](#success-criteria-3)
- [File Changes](#file-changes)
- [Testing Plan](#testing-plan)
- [Open Questions](#open-questions)
- [Dependencies](#dependencies)
- [References](#references)
<!--toc:end-->

## Objective

Replace `internal/search/meilisearch/section.go:slugify` with a Go port of `github-slugger`, the algorithm `rehype-slug` (and GitHub itself) uses for heading anchors. Add per-document collision-suffix tracking on the indexer side, and a CI-enforced contract test that pins the algorithm to a snapshot fixture generated from the upstream JS implementation. Closes the implicit-and-broken contract surfaced by [INV-0002][inv-0002] / [issue #20][issue-20].

**Implements:** [INV-0002 #Recommendation][inv-0002], [issue #20 acceptance criteria][issue-20].

## Scope

### In Scope

- New pure `slug` function with github-slugger-faithful semantics (Unicode-aware keep set, no trim, single-space → hyphen).
- Stateful `slugger` struct wired per-document through `splitSections` in `internal/search/meilisearch/`.
- Snapshot fixture generated once from upstream `github-slugger` (committed as JSON) and a Go test that asserts the port matches it byte-for-byte.
- Updates to existing `section_test.go` and `indexer_test.go` slug expectations.
- `make reindex --check-drift` rollout step + documentation in the PR body and CHANGELOG.

### Out of Scope

- OpenAPI schema changes — `section_slug` is `type: string` today and stays that way; no new constraint needed at the wire level.
- Changes to `cmd/rfc-api/reindex.go` itself; existing infrastructure handles the migration.
- rfc-site-side enforcement. rfc-site's CI runs the same fixture against actual `github-slugger` independently; we don't vendor their tests here.
- `maintainCase` parameter from upstream github-slugger. We always lowercase. If a future caller needs case-preserved slugs, that's a follow-up — see [#Open Questions](#open-questions).
- Performance optimization. The new regex (`\p{L}\p{N}`) is microseconds slower per call but indexing is bounded by Meili task latency, not local CPU. Skip benchmarking unless ops finds a regression.

## Implementation Phases

Each phase builds on the previous one. A phase is complete when all its tasks are checked off and its success criteria are met. Recommended PR boundary: ship Phases 1–3 in one PR (algorithm + state + tests are tightly coupled); Phase 4 in a second, separately-reviewable PR that's purely the rollout.

---

### Phase 1: Algorithm port — pure github-slugger-faithful slug

Replace the existing `slugify` body with a Go port of `github-slugger`'s pure `slug(value)` function. Keep the function signature so existing callers compile unchanged. Update unit-test expectations to match the new behavior.

#### Tasks

- [ ] Add `var keepRune = regexp.MustCompile(\`[^\p{L}\p{N}_\- ]\`)` at the top of `section.go` (replacing `nonSlugRune`).
- [ ] Rewrite `slugify` (rename to `slug` per github-slugger naming, leave `slugify` as a one-line alias if any external caller depends on it):
  ```go
  func slug(s string) string {
      s = strings.ToLower(s)
      s = keepRune.ReplaceAllString(s, "")
      s = strings.ReplaceAll(s, " ", "-")
      return s
  }
  ```
  Note: no leading/trailing `TrimSpace`, no post-strip `Trim("-")`. Both diverge from github-slugger.
- [ ] Update `section_test.go`'s existing slug-related cases to the new outputs (the `simple-heading` / `first` cases stay the same; any case that exercised trimming, underscore stripping, or Unicode needs updating).
- [ ] Add new table-driven test cases covering the divergence classes from INV-0002 #Findings: apostrophe, period, ampersand, em dash, underscore, leading/trailing space, multiple consecutive spaces, Latin-1 / CJK / Cyrillic / Greek letters, all-stripped input, empty input.
- [ ] Run `make lint` and `make fmt`; fix any new warnings.

#### Success Criteria

- `go test ./internal/search/meilisearch/...` passes with the new algorithm and fixtures.
- Every new unit-test case in `section_test.go` corresponds to a row in INV-0002's #Findings table.
- `make lint` clean.
- The package-level `slug` is pure and stateless — no package-level `seen` map, no globals beyond the regex.

---

### Phase 2: Stateful per-document slugger + indexer wiring

The pure `slug` function isn't enough — `rehype-slug` adds collision suffixing per rendered document. Add a `slugger` struct that tracks occurrences per document, and route `splitSections` through it.

#### Tasks

- [ ] Add a `slugger` struct in `section.go`:
  ```go
  type slugger struct{ seen map[string]int }
  func newSlugger() *slugger { return &slugger{seen: map[string]int{}} }
  func (g *slugger) slug(s string) string {
      base := slug(s)
      result := base
      for {
          if _, exists := g.seen[result]; !exists {
              break
          }
          g.seen[base]++
          result = fmt.Sprintf("%s-%d", base, g.seen[base])
      }
      g.seen[result] = 0
      return result
  }
  ```
  Faithfully port the upstream loop semantics — note that `seen[base]` increments while `result` is composed from the (possibly already-suffixed) string, matching github-slugger's behavior.
- [ ] Modify `splitSections` to instantiate `g := newSlugger()` once per call and use `g.slug(heading)` instead of the package-level `slug`. (One slugger per document, not one per package; matches `rehype-slug`'s per-HAST-tree behavior.)
- [ ] Add a unit test in `section_test.go` asserting that `splitSections` on a synthetic document with three `## Notes` H2s produces sub-doc slugs `notes`, `notes-1`, `notes-2` in that order.
- [ ] Add a unit test asserting that running `splitSections` twice on the same body produces identical slug sequences (state resets per call).
- [ ] Update `indexer_test.go` if any of its fixtures used duplicate H2s and now produce different sub-doc ids.
- [ ] Sanity-check that the Meili sub-doc id construction (`{parent}__{slug}`) still passes Meili's id charset — collision suffixes are `{base}-{N}`, all in `[a-z0-9_-]` per the new keep set, so the existing `__` separator is still safe.
- [ ] `make lint`, `make fmt`.

#### Success Criteria

- `splitSections` produces unique slugs for repeat-heading documents; no two sections share a Meili sub-doc id.
- Per-document state isolation verified (no cross-document leakage between calls).
- Existing `internal/search/meilisearch/` unit tests all green.
- Meili sub-doc id charset constraint (`[a-zA-Z0-9_-]`, ≤511 bytes) still satisfied for any conceivable input.

---

### Phase 3: Snapshot-fixture contract test

Pin the algorithm to a snapshot generated from upstream `github-slugger`. The fixture is the ground truth; both rfc-api and rfc-site assert against the same file in their respective CI pipelines.

#### Tasks

- [ ] Curate an input list of ~50 heading strings covering: ASCII basic, ASCII with each common punctuation class, leading/trailing whitespace, multi-word with extra spacing, code-span text (post-AST), inline-formatting text, Latin Extended (Café, naïve, Mañana), CJK (日本語, 中文, 한국어), Cyrillic, Greek, mixed scripts, length-1, all-stripped, empty string, headings with the `X / Y` pattern, headings with em dashes. Commit as `internal/search/meilisearch/testdata/slug_fixtures_input.json` (just the list of strings).
- [ ] Run upstream `github-slugger` once locally:
  ```sh
  cd /tmp && npm init -y && npm i github-slugger
  node -e '
    const Slugger = (await import("github-slugger")).default;
    const inputs = require("/path/to/slug_fixtures_input.json");
    const out = inputs.map((h, i, arr) => {
      // use the stateless slug() for non-collision cases
      // for collision testing, group by a "scope" marker (TBD format)
      return [h, /* ... */];
    });
    process.stdout.write(JSON.stringify(out, null, 2));
  '
  ```
  Decide fixture file shape — see [#Open Questions](#open-questions) Q2.
- [ ] Commit the snapshot as `internal/search/meilisearch/testdata/slug_fixtures.json` with each row `{input, want_pure}` (and, for collision cases, `{scope, sequence: [{input, want}, ...]}`).
- [ ] Add `slug_contract_test.go` in `internal/search/meilisearch/` (or `test/contract/` — see Q1):
  - Loads `slug_fixtures.json`.
  - Asserts `slug(input) == want_pure` for each pure-call case.
  - Asserts the stateful `slugger` produces the expected sequence for each collision-scope case.
  - Test failure messages include `(input=…, want=…, got=…)` so the diff is obvious.
- [ ] Document the snapshot-regen procedure as a one-shot script (`scripts/regen-slug-fixtures.sh` or a Make target), so future updates to `github-slugger` can be picked up deliberately. Per memory: toolchain version bumps are deliberate, not silent.
- [ ] Verify the test runs in CI on every push (it will, since it's a regular `go test` under existing `test-go` job — no workflow change needed).

#### Success Criteria

- `internal/search/meilisearch/testdata/slug_fixtures.json` exists, generated from upstream `github-slugger`, committed.
- Contract test passes locally and in CI.
- Test failure messages name the specific heading and the rfc-api vs expected divergence.
- Regen script documented in `docs/development/` (or wherever rebuild instructions live) so a future developer can pick up upstream updates without re-deriving the procedure.

---

### Phase 4: Migration + rollout coordination

The slug change invalidates every existing Meili sub-doc id whose heading used Unicode letters, underscores, apostrophes, em dashes, the `X / Y` pattern, or duplicate H2s. Reindex the live index after deploy.

#### Tasks

- [ ] Run `./build/bin/rfc-api reindex --check-drift` against a dev instance pre-deploy; record the divergence count for the PR description.
- [ ] Bump the binary version (`make release TAG=…`) per RFC-0001 #Versioning. This is a behavior change to a public-ish field (the value of `section_slug`), so it warrants at minimum a patch bump and an explicit CHANGELOG entry.
- [ ] PR body / CHANGELOG entry calls out: "**Behavior change**: `section_slug` algorithm now matches `github-slugger`. Run `make reindex` against any populated Meili instance after upgrading."
- [ ] Coordinate with rfc-site: bump rfc-api OpenAPI pin in rfc-site, regenerate types (no schema change but the values shift), redeploy.
- [ ] After both deploys, run `rfc-api reindex --check-drift` against staging/prod; confirm no drift.
- [ ] Close issue #20 with a link to this PR.

#### Success Criteria

- Drift check returns clean (zero divergence between Postgres and Meili) post-reindex on every populated instance.
- rfc-site search-result deep-links scroll to the correct heading anchor on a representative set of 5+ documents covering the divergence classes (apostrophe, em dash, `X / Y`, duplicate H2, Unicode letter).
- CHANGELOG / release notes record the behavior change.
- Issue #20 closed.

## File Changes

| File                                                     | Action | Description |
|----------------------------------------------------------|--------|-------------|
| `internal/search/meilisearch/section.go`                 | Modify | Replace `nonSlugRune` + `slugify`; add `slug`, `slugger`, `newSlugger`. Wire `splitSections` to use a per-call slugger. |
| `internal/search/meilisearch/section_test.go`            | Modify | Update existing cases; add table-driven tests for divergence classes and collision suffixing. |
| `internal/search/meilisearch/indexer_test.go`            | Modify | Update fixtures if any rely on the old `slugify` output. |
| `internal/search/meilisearch/testdata/slug_fixtures_input.json` | Create | Curated input headings (no algorithm output — just the strings). |
| `internal/search/meilisearch/testdata/slug_fixtures.json`       | Create | Snapshot of `github-slugger` output for the input list. |
| `test/contract/slug_test.go`                             | Create | Asserts the Go port matches the snapshot byte-for-byte. Lives in `test/contract/` per Q1 — single home for all contract tests. |
| `scripts/regen-slug-fixtures.sh` *(or Make target)*      | Create | One-shot regen against upstream `github-slugger`. |
| `CHANGELOG.md` / release notes                           | Modify | Behavior-change callout for the deploy. |

## Testing Plan

- **Unit tests** — table-driven, in `section_test.go`. Cover every divergence class from INV-0002 #Findings.
- **Collision tests** — synthetic doc with N>1 of the same H2; assert sequence.
- **Contract test** — against the committed snapshot fixture; runs on every push under existing `test-go` CI job.
- **Integration test** — extend `internal/search/meilisearch/*_test.go` (build-tag `integration`) to verify a document with collision-prone H2s indexes without sub-doc id collisions and is queryable per-section. CI's `meilisearch:v1.20` service container already supports this.
- **Manual rollout sanity** — Phase 4 task list includes the human-driven check ("does scroll-to-anchor work on real search hits").

## Open Questions

For review before implementation starts. Each question presents distinct, mutually-exclusive options. The **Lean** line names the option I'd pick if forced today; flip or amend in review.

---

### Q1. Where does the contract test live?

- **A.** `internal/search/meilisearch/slug_contract_test.go` — colocated with the implementation.
  *Pro:* a developer touching `section.go` sees the contract test in the same directory.
  *Con:* not under `test/contract/`, which is the established home for cross-repo contract tests.
- **B.** `test/contract/slug_test.go` — alongside the existing OpenAPI-shape contract tests.
  *Pro:* consistent "this contract spans repos" location.
  *Con:* `test/contract/` today is OpenAPI-shape-only; mixing in a behavior contract changes its meaning.

**Lean: A.** The OpenAPI contract tests are about wire-format shape; this is about a value-level invariant of `section_slug`. Different category; colocation makes the implementation↔contract link tighter.

**Selected: B** — keep all contract tests in one home (`test/contract/`) so a future grep or new contributor finds them in one place. The "OpenAPI-only" framing of `test/contract/` was incidental, not load-bearing.

---

### Q2. Snapshot fixture format.

- **A.** Two-section JSON with pure cases and collision groups separated:
  ```json
  {
    "pure": [
      {"input": "Café", "expected": "café"},
      {"input": "Hello, World!", "expected": "hello-world"}
    ],
    "collision_groups": [
      {
        "name": "duplicate-h2-notes",
        "sequence": [
          {"input": "Notes", "expected": "notes"},
          {"input": "Notes", "expected": "notes-1"},
          {"input": "Notes", "expected": "notes-2"}
        ]
      }
    ]
  }
  ```
- **B.** Single flat list with an explicit `scope` field; collision sequencing implied by ordering within the same scope:
  ```json
  [
    {"scope": "pure", "input": "Café", "expected": "café"},
    {"scope": "doc:notes", "input": "Notes", "expected": "notes"},
    {"scope": "doc:notes", "input": "Notes", "expected": "notes-1"}
  ]
  ```
- **C.** YAML rather than JSON (more readable for the Unicode + multi-line cases).

**Lean: A.** The pure/collision split makes the test's structure obvious from one glance at the file; explicit groups beat order-dependent encoding for diff-friendliness.

**Selected: A** — two-section JSON. Diff-friendly, structure is visible at a glance, native output of `npx github-slugger` so no transform step.

---

### Q3. Should rfc-site share this fixture file?

- **A.** Vendor a copy in rfc-site's repo. Regen procedure documented in both READMEs. A hash check in both CIs detects drift between the two copies.
  *Pro:* maximally robust — any drift fails fast.
  *Con:* tightly couples the repos; vendoring chore on every regen.
- **B.** Each repo independently re-derives from upstream `github-slugger`. rfc-api's CI asserts `Go port == snapshot`; rfc-site's CI asserts `npx github-slugger == snapshot` against its own copy.
  *Pro:* loosely coupled; regen produces byte-identical output on both sides anyway.
  *Con:* a 2-line skew in regen scripts could go undetected briefly.
- **C.** Single source of truth: rfc-api hosts the fixture, rfc-site fetches it at build time (e.g. via `git submodule` or a `npm` package).
  *Pro:* truly one file.
  *Con:* adds a build-time dependency; release-time coordination overhead.

**Lean: B.** Upstream releases rarely. Both regen scripts pin the same upstream version. Independent enforcement keeps the repos loosely coupled, which matters more than a hypothetical regen-skew that the snapshot would catch in the next regen anyway.

**Selected: B** — independently re-derive in each repo. Both regen scripts pin the same upstream version; loose coupling between repos preserved.

---

### Q4. Backwards-compat during the reindex window.

After deploy + before reindex finishes, `section_slug` values from search responses use the new algorithm but Meili sub-doc ids are still old (or vice versa, depending on which side ships first).

- **A.** Accept the brief broken window. Internal-network-only tool; reindex completes in seconds for the current corpus.
  *Effect:* search hits land on the right doc but scroll-to-section may fail for a minute or two.
- **B.** Coordinate deploy: drain worker, reindex, then bring serve back.
  *Effect:* ~2 minutes of read downtime, no broken-slug window.
- **C.** Feature-flag the new algorithm; dual-emit for one release; flip the flag in a follow-up.
  *Effect:* zero broken window; adds branching in `section.go` for one release cycle.

**Lean: A.** Reindex is sub-minute on the current corpus, audience is internal, and the worst-case UX (scroll didn't happen) is graceful, not broken.

**Selected: A** — accept the sub-minute broken-scroll window. Worst-case UX is graceful (the doc still loads, the right page still matches the hit); reindex is fast on this corpus.

---

### Q5. `\p{L}\p{N}` vs upstream's precomputed Unicode regex.

- **A.** Use Go's native `\p{L}\p{N}_\- ` keep set.
  *Pro:* 1-line regex, leverages Go's Unicode tables, faithful for practical heading content.
  *Con:* differs from upstream on a handful of edge codepoints (musical symbols, some historic scripts, certain emoji ranges) that don't appear in real prose.
- **B.** Vendor the upstream `regex.js` precomputed character class verbatim (port the giant blob to a Go raw-string `regexp.MustCompile`).
  *Pro:* byte-perfect parity even on edge codepoints.
  *Con:* ~14 KB of opaque generated regex source in our codebase; updates require re-running upstream's `script/` against the latest Unicode database.

**Lean: A.** The snapshot fixture catches any divergence the moment it actually shows up in real headings. Until then, the precomputed regex is dead weight that our developers can't read.

**Selected: A** — `\p{L}\p{N}_\- `. 1-line readable regex; snapshot catches any future divergence.

---

### Q6. Inline HTML / non-Text inlines in headings.

`headingText` (`section.go:98`) today walks goldmark `*ast.Text` segments and one level of children. It does *not* recurse into `*ast.RawHTML` or `*ast.AutoLink`. rehype-slug operates on rendered HTML, so for `## <span>foo</span> bar` it sees `foo bar`. We need to confirm what rfc-api currently produces.

- **A.** Verify behavior, leave as-is if it matches.
  *Action:* add a fixture case `## <span>foo</span> bar` to the snapshot, run; if rfc-api matches, done.
- **B.** Verify behavior, file a follow-up if it diverges, ship this IMPL anyway.
  *Action:* same verification; if it diverges, open a separate issue + add a `t.Skip` for that case in this PR.
- **C.** Fix `headingText` in this IMPL to recurse into RawHTML segments before the snapshot is generated.
  *Pro:* one less follow-up.
  *Con:* expands the scope of this IMPL beyond the slug algorithm.

**Lean: B.** Inline HTML in Markdown headings is rare in this corpus and not a regression — the contract test will surface it later if it ever appears. Keep this IMPL focused.

**Selected: B** — verify, file a follow-up if it diverges, ship with `t.Skip` for that one case. Keeps this IMPL's scope crisp on the slug algorithm; `headingText` is a separate function with separate review surface.

---

### Q7. Add an explicit OpenAPI constraint on `section_slug`?

Today it's `type: string`. We could add a `pattern:` to encode the contract at the wire level.

- **A.** Add `pattern: "^[\\p{L}\\p{N}_-]+(-[0-9]+)?$"` (or a more-permissive ASCII fallback).
  *Pro:* machine-checkable from any client.
  *Con:* OpenAPI 3.1 / `kin-openapi` regex support for `\p{L}` is unreliable; we may have to fall back to `[a-zA-Z0-9_\-]` which under-constrains.
- **B.** Leave the schema as `type: string`; keep the contract in the snapshot fixture.
  *Pro:* no `kin-openapi` portability headaches.
  *Con:* no client-side schema enforcement.

**Lean: B.** The behavior contract belongs in the snapshot fixture, where it's enforced by both producer and consumer CI. The OpenAPI schema isn't the right tool for "this string was produced by github-slugger."

**Selected: B** — leave the schema as `type: string`. The behavior contract belongs in the snapshot fixture where it's actually enforced, not in a regex that `kin-openapi` may or may not honor faithfully.

---

### Q8. Naming: `slugify` vs `slug`.

The existing function is `slugify`. Upstream `github-slugger` calls it `slug`. The field on the wire is `section_slug`.

- **A.** Rename `slugify` → `slug`. Matches upstream, matches the field name. Internal-only function; no API break.
- **B.** Keep `slugify` for the existing function and add a new internal `slug` alias.
  *Pro:* zero diff churn outside the function body.
  *Con:* two names for one thing in the same package.
- **C.** Keep `slugify` everywhere; treat the upstream-naming alignment as cosmetic.

**Lean: A.** Internal package function, single call site, one-line rename. The naming alignment is small but real value when reading the code against the upstream reference.

**Selected: A** — rename `slugify` → `slug`. Matches upstream `github-slugger` and the wire-field `section_slug`; one-line internal rename.

## Dependencies

- INV-0002 (this repo) — Concluded with Recommendation Option A.
- Issue #20 — acceptance criteria define the contract-test shape.
- rfc-site — must regenerate types from rfc-api's OpenAPI spec post-deploy and (if going with Q3 lean) maintain its own snapshot fixture against upstream `github-slugger`.
- Upstream `github-slugger` (currently 2.x) — pinning a specific version in the regen script keeps the snapshot reproducible.

No blockers in this repo. Phases 1–3 can proceed as soon as Open Questions resolve. Phase 4 needs coordination with rfc-site's deploy cadence.

## References

- [INV-0002 — section_slug consumer-side slug contract](../investigation/0002-sectionslug-consumer-side-slug-contract.md)
- [Issue #20 — Contract test: assert section_slug equals rehype-slug(section_heading)](https://github.com/donaldgifford/rfc-api/issues/20)
- [`github-slugger` upstream](https://github.com/Flet/github-slugger)
- [`rehype-slug` upstream](https://github.com/rehypejs/rehype-slug)
- [ADR-0003 — Use Meilisearch for rfc-api search](../adr/0003-use-meilisearch-for-rfc-api-search.md) — `section_slug` introduced as part of per-section indexing.
- `internal/search/meilisearch/section.go` — current `slugify` implementation.
- `cmd/rfc-api/reindex.go` — `--check-drift` infrastructure used in Phase 4.

[inv-0002]: ../investigation/0002-sectionslug-consumer-side-slug-contract.md
[issue-20]: https://github.com/donaldgifford/rfc-api/issues/20
