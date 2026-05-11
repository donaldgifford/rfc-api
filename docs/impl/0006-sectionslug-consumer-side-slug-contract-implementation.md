---
id: IMPL-0006
title: "section_slug consumer-side slug contract implementation"
status: In Progress
author: Donald Gifford
created: 2026-05-08
---
<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0006: section_slug consumer-side slug contract implementation

**Status:** In Progress — Open Questions resolved 2026-05-11; implementation pending.
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
  - [Q1. Where does the contract test live?](#q1-where-does-the-contract-test-live)
  - [Q2. Snapshot fixture format.](#q2-snapshot-fixture-format)
  - [Q3. Should rfc-site share this fixture file?](#q3-should-rfc-site-share-this-fixture-file)
  - [Q4. Backwards-compat during the reindex window.](#q4-backwards-compat-during-the-reindex-window)
  - [Q5. \p{L}\p{N} vs upstream's precomputed Unicode regex.](#q5-plpn-vs-upstreams-precomputed-unicode-regex)
  - [Q6. Inline HTML / non-Text inlines in headings.](#q6-inline-html--non-text-inlines-in-headings)
  - [Q7. Add an explicit OpenAPI constraint on section_slug?](#q7-add-an-explicit-openapi-constraint-on-sectionslug)
  - [Q8. Naming: slugify vs slug.](#q8-naming-slugify-vs-slug)
- [Dependencies](#dependencies)
- [References](#references)
<!--toc:end-->

## Objective

Replace `internal/search/meilisearch/section.go:slugify` with a Go port of `github-slugger`, the algorithm `rehype-slug` (and GitHub itself) uses for heading anchors. Add per-document collision-suffix tracking on the indexer side, and a CI-enforced contract test that pins the algorithm to a snapshot fixture generated from the upstream JS implementation. Closes the implicit-and-broken contract surfaced by [INV-0002][inv-0002] / [issue #20][issue-20].

**Implements:** [INV-0002 #Recommendation][inv-0002], [issue #20 acceptance criteria][issue-20].

## Scope

### In Scope

- New `internal/slug/` package with a pure `Slug` function (github-slugger-faithful semantics: Unicode-aware keep set, no trim, single-space → hyphen) and a stateful `Slugger` type for collision suffixing.
- `internal/search/meilisearch/splitSections` rewired to instantiate `slug.NewSlugger()` per document.
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

Replace the existing `slugify` body with a Go port of `github-slugger`'s pure `slug(value)` function. The pure function moves to a new tiny package `internal/slug/` so the cross-package contract test under `test/contract/` (Q1=B) can call it without exporting implementation-detail symbols from `internal/search/meilisearch/`. `meilisearch` becomes a single-import consumer of the new package.

#### Tasks

- [x] Create `internal/slug/slug.go`:
  ```go
  // Package slug implements GitHub-flavored heading slugification,
  // faithful to upstream github-slugger / rehype-slug. See IMPL-0006.
  package slug

  import (
      "regexp"
      "strings"
  )

  var keepRune = regexp.MustCompile(`[^\p{L}\p{N}_\- ]`)

  // Slug returns the github-slugger slug for s. Pure, stateless.
  // Lowercases, strips runs of disallowed runes, converts single
  // spaces to hyphens. No trim.
  func Slug(s string) string {
      s = strings.ToLower(s)
      s = keepRune.ReplaceAllString(s, "")
      s = strings.ReplaceAll(s, " ", "-")
      return s
  }
  ```
- [x] Delete the old `nonSlugRune` regex and `slugify` function from `internal/search/meilisearch/section.go`. Update the single call site in `splitSections` to call `slug.Slug(...)` (Phase 2 will replace this again with the per-document stateful slugger).
- [x] Add `internal/slug/slug_test.go` with table-driven cases for the divergence classes from INV-0002 #Findings: apostrophe, period, ampersand, em dash, underscore, leading/trailing space, multiple consecutive spaces, Latin-1 / CJK / Cyrillic / Greek letters, all-stripped input, empty input. (These are *unit* tests on the pure function; the snapshot-driven *contract* test comes in Phase 3.)
- [x] Update `internal/search/meilisearch/section_test.go`'s existing slug-related cases — the `simple-heading` / `first` cases stay the same; any case that exercised trimming, underscore stripping, or Unicode needs updating. Add `import "github.com/donaldgifford/rfc-api/internal/slug"` if the test calls the slug function directly.
- [x] Run `make lint` and `make fmt`; fix any new warnings.

#### Success Criteria

- `go test ./internal/slug/... ./internal/search/meilisearch/...` passes.
- Every new unit-test case in `internal/slug/slug_test.go` corresponds to a row in INV-0002's #Findings table.
- `make lint` clean.
- `slug.Slug` is pure and stateless — no package-level state, no globals beyond `keepRune`. The function does *not* `TrimSpace` or post-strip `Trim("-")`; both diverge from github-slugger.

---

### Phase 2: Stateful per-document slugger + indexer wiring

The pure `Slug` function isn't enough — `rehype-slug` adds collision suffixing per rendered document. Add a `Slugger` type in the same `internal/slug/` package, and route `splitSections` through a fresh instance per call.

#### Tasks

- [x] Extend `internal/slug/slug.go` with the stateful slugger:
  ```go
  // Slugger tracks slug occurrences within one document so duplicate
  // headings collide deterministically into base, base-1, base-2, ...
  // Matches github-slugger's per-Slugger-instance behavior.
  type Slugger struct{ seen map[string]int }

  // NewSlugger returns a fresh Slugger with no recorded occurrences.
  func NewSlugger() *Slugger { return &Slugger{seen: map[string]int{}} }

  // Slug returns the slug for s, suffixed with -1, -2, ... if it
  // collides with a previous call on this Slugger.
  func (g *Slugger) Slug(s string) string {
      base := Slug(s)
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
  Faithfully ports the upstream loop semantics: `seen[base]` increments while `result` is composed from the unsuffixed `base`, matching github-slugger's `originalSlug + '-' + occurrences[originalSlug]`. Edge case to preserve: if `Slugger.Slug("Notes-1")` is called explicitly *after* `Slugger.Slug("Notes")`, the third `Slugger.Slug("Notes")` call must return `notes-2`, not `notes-1` — upstream handles this because the while loop re-checks after each suffix. Cover this in a test.
- [x] Modify `internal/search/meilisearch/section.go:splitSections` to instantiate `g := slug.NewSlugger()` once per call and use `g.Slug(heading)` instead of `slug.Slug(...)`. One slugger per document, not one per package; matches `rehype-slug`'s per-HAST-tree behavior.
- [x] Add `internal/slug/slug_test.go` cases for the stateful slugger:
  - Three calls with the same input emit `notes`, `notes-1`, `notes-2`.
  - The pre-existing-suffix edge case (`Notes`, `Notes-1`, `Notes`) emits `notes`, `notes-1`, `notes-2`.
  - Two separate `Slugger` instances don't share state.
- [x] Add a `section_test.go` case for `splitSections` asserting that a synthetic document with three `## Notes` H2s emits sub-doc slugs `notes`, `notes-1`, `notes-2` in that order.
- [x] Add a `section_test.go` case asserting that two `splitSections` calls on the same body produce identical slug sequences (state is per-call, not cross-call).
- [x] Update `indexer_test.go` if any fixtures used duplicate H2s and now produce different sub-doc ids. *(No duplicate-H2 fixtures existed; expected slug values like `"first"` are stable under the new algorithm.)*
- [x] Sanity-check that the Meili sub-doc id construction (`{parent}__{slug}`) still passes Meili's id charset — collision suffixes are `{base}-{N}`, all in `[a-z0-9_-]` per the new keep set, so the existing `__` separator is still safe.
- [x] `make lint`, `make fmt`.

#### Success Criteria

- `splitSections` produces unique slugs for repeat-heading documents; no two sections share a Meili sub-doc id.
- `slug.Slugger` instance isolation verified — two separate `Slugger` instances do not share state, and `splitSections` resets cleanly between calls.
- Pre-existing-suffix edge case covered: `Notes`, `Notes-1`, `Notes` emits `notes`, `notes-1`, `notes-2` (not `notes`, `notes-1`, `notes-1`).
- All `internal/slug/` and `internal/search/meilisearch/` unit tests green.
- Meili sub-doc id charset constraint (`[a-zA-Z0-9_-]`, ≤511 bytes) still satisfied for any conceivable input.

---

### Phase 3: Snapshot-fixture contract test

Pin the algorithm to a snapshot generated from upstream `github-slugger`. The fixture is the ground truth; both rfc-api and rfc-site assert against the same file in their respective CI pipelines.

#### Tasks

- [x] Curate an input source file covering the categories below. Two top-level keys mirroring the fixture shape (Q2=A): `pure` (~40 plain heading strings) and `collision_groups` (~5 named groups, each a sequence of repeats). Commit as `test/contract/testdata/slug_fixtures_input.json`.
  - **Pure categories:** ASCII basic, each common punctuation class (apostrophe, period, ampersand, em dash, slash with surrounding spaces), leading/trailing whitespace, multi-word with extra spacing, code-span text (post-AST: e.g. `The Source field`), Latin Extended (Café, naïve, Mañana), CJK (日本語, 中文, 한국어), Cyrillic, Greek, mixed scripts, length-1, all-stripped, empty string.
  - **Collision categories:** duplicate H2 text (`Notes` × 3), duplicate after suffix collision (`Notes` × 2 then `Notes-1` × 1 — confirms suffixing doesn't double-suffix), mixed-script repeats.
- [x] Run upstream `github-slugger` once locally to produce the snapshot — pin a specific upstream version (e.g. `github-slugger@2.0.0`) in the regen script so the snapshot is reproducible. Pseudocode:
  ```sh
  cd "$(mktemp -d)"
  npm init -y >/dev/null
  npm i github-slugger@2.0.0 >/dev/null
  node -e '
    const GithubSlugger = (await import("github-slugger")).default;
    const { slug } = await import("github-slugger");
    const inputs = require(process.argv[1]);
    const out = {
      pure: inputs.pure.map(s => ({input: s, expected: slug(s)})),
      collision_groups: inputs.collision_groups.map(g => {
        const sl = new GithubSlugger();
        return {
          name: g.name,
          sequence: g.sequence.map(s => ({input: s, expected: sl.slug(s)}))
        };
      })
    };
    process.stdout.write(JSON.stringify(out, null, 2) + "\n");
  ' "$INPUT_PATH" > test/contract/testdata/slug_fixtures.json
  ```
- [x] Commit the snapshot as `test/contract/testdata/slug_fixtures.json` — two-section shape per Q2=A (`pure[]` of `{input, expected}` rows + `collision_groups[]` of `{name, sequence[]}` rows).
- [x] Add `test/contract/slug_test.go` (Q1=B — single home for all contract tests):
  - Loads `testdata/slug_fixtures.json`.
  - Asserts `slug.Slug(input) == expected` for each `pure` row (imports the new `internal/slug` package introduced in Phase 1).
  - For each `collision_groups[i]`, instantiates `slug.NewSlugger()`, walks the `sequence`, asserts each step matches.
  - Test failure messages include `(group=…, step=…, input=…, want=…, got=…)` so the diff is obvious.
- [x] Add `scripts/regen-slug-fixtures.sh` (and a `make regen-slug-fixtures` target wrapping it) implementing the Node-script flow above. Pins the upstream version explicitly so future updates are deliberate, not silent.
- [x] Verify the test runs in CI on every push (it will — regular `go test ./test/contract/...` under the existing `test-go` job; no workflow change needed).

#### Success Criteria

- `test/contract/testdata/slug_fixtures.json` exists, generated from a pinned upstream `github-slugger` version, committed.
- `test/contract/slug_test.go` passes locally and in CI; runs under the existing `test-go` job.
- Test failure messages name the specific group + input + expected vs got.
- `make regen-slug-fixtures` + `scripts/regen-slug-fixtures.sh` exist and are mentioned from `docs/development/` so a future developer can pick up upstream updates by running one command.

---

### Phase 4: Migration + rollout coordination

The slug change invalidates every existing Meili sub-doc id whose heading used Unicode letters, underscores, apostrophes, em dashes, the `X / Y` pattern, or duplicate H2s. Reindex the live index after deploy.

#### Tasks

- [ ] Run `./build/bin/rfc-api reindex --check-drift` against a dev instance pre-deploy; record the divergence count for the PR description.
- [ ] Bump the binary version (`make release TAG=…`) per RFC-0001 #Versioning. This is a behavior change to a public-ish field (the value of `section_slug`), so it warrants at minimum a patch bump and an explicit CHANGELOG entry.
- [ ] PR body / CHANGELOG entry calls out: "**Behavior change**: `section_slug` algorithm now matches `github-slugger`. Run `make reindex` against any populated Meili instance after upgrading."
- [ ] Coordinate with rfc-site: bump rfc-api OpenAPI pin in rfc-site, regenerate types (no schema change but the slug values shift), redeploy. rfc-site also re-runs its own snapshot-fixture regen against the same pinned upstream `github-slugger` version per Q3=B (independent re-derivation), and adds the same `make regen-slug-fixtures` equivalent on its side if not already present.
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
| `internal/slug/slug.go`                                  | Create | New package. Pure `Slug(string) string` + stateful `Slugger` / `NewSlugger`. Exported so the `test/contract/` test can call them. |
| `internal/slug/slug_test.go`                             | Create | Unit tests for the pure function (divergence classes) and the stateful slugger (collision sequences, pre-existing-suffix edge case, instance isolation). |
| `internal/search/meilisearch/section.go`                 | Modify | Delete `nonSlugRune` + `slugify`. `splitSections` instantiates `slug.NewSlugger()` per call and uses `g.Slug(heading)`. |
| `internal/search/meilisearch/section_test.go`            | Modify | Update existing slug-related cases; add `splitSections` collision-suffix cases. |
| `internal/search/meilisearch/indexer_test.go`            | Modify | Update fixtures if any rely on the old `slugify` output. |
| `test/contract/testdata/slug_fixtures_input.json`        | Create | Curated input categories (two-section shape: `pure[]` strings + `collision_groups[]` named sequences). |
| `test/contract/testdata/slug_fixtures.json`              | Create | Snapshot of `github-slugger` output for the input file. Two-section JSON per Q2=A. |
| `test/contract/slug_test.go`                             | Create | Asserts `slug.Slug` / `slug.Slugger` match the snapshot byte-for-byte. Lives in `test/contract/` per Q1=B — single home for all contract tests. |
| `scripts/regen-slug-fixtures.sh` + `make regen-slug-fixtures` | Create | One-shot regen against a pinned upstream `github-slugger` version. |
| `CHANGELOG.md` / release notes                           | Modify | Behavior-change callout for the deploy. |

## Testing Plan

- **Unit tests (pure function)** — table-driven, in `internal/slug/slug_test.go`. Cover every divergence class from INV-0002 #Findings.
- **Unit tests (stateful slugger)** — same file; collision sequences, pre-existing-suffix edge case, instance isolation.
- **Integration test (splitSections)** — `internal/search/meilisearch/section_test.go`; synthetic doc with N>1 of the same H2; assert the slug sequence flows through the AST walk.
- **Contract test** — against the committed snapshot fixture; runs on every push under existing `test-go` CI job.
- **Integration test** — extend `internal/search/meilisearch/*_test.go` (build-tag `integration`) to verify a document with collision-prone H2s indexes without sub-doc id collisions and is queryable per-section. CI's `meilisearch:v1.20` service container already supports this.
- **Manual rollout sanity** — Phase 4 task list includes the human-driven check ("does scroll-to-anchor work on real search hits").

## Open Questions

**All 8 resolved 2026-05-11.** Each question records the options considered, the Lean at draft time, and the **Selected** answer. Implementation proceeds against the selections.

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
- rfc-site — must regenerate types from rfc-api's OpenAPI spec post-deploy and maintain its own snapshot fixture against the same pinned upstream `github-slugger` version (Q3=B).
- Upstream `github-slugger` (pin to 2.x; specific version recorded in `scripts/regen-slug-fixtures.sh`) — pinning keeps the snapshot reproducible across both repos.

No blockers in this repo. Open Questions resolved 2026-05-11; Phases 1–3 ready to start. Phase 4 needs coordination with rfc-site's deploy cadence.

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
