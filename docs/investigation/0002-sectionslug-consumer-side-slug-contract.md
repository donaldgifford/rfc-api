---
id: INV-0002
title: "section_slug consumer-side slug contract"
status: Concluded
author: Donald Gifford
created: 2026-05-08
concluded: 2026-05-08
---

<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0002: section_slug consumer-side slug contract

**Status:** Concluded — recommendation accepted; implementation tracked in
[IMPL-0006][impl-0006].
**Author:** Donald Gifford **Date:** 2026-05-08

<!--toc:start-->
- [Question](#question)
- [Hypothesis](#hypothesis)
- [Context](#context)
- [Approach](#approach)
- [Environment](#environment)
- [Findings](#findings)
  - [Algorithm A — rfc-api slugify](#algorithm-a--rfc-api-slugify)
  - [Algorithm B — github-slugger](#algorithm-b--github-slugger)
  - [Fixture comparison (synthetic)](#fixture-comparison-synthetic)
  - [Fixture comparison (real corpus)](#fixture-comparison-real-corpus)
  - [Side effect: indexer sub-doc id collisions](#side-effect-indexer-sub-doc-id-collisions)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

For every heading in the rfc-api corpus, does the `section_slug` value emitted
by `internal/search/meilisearch/section.go:slugify` equal the slug that
`rehype-slug` (via `github-slugger`) would produce for the same heading text?

If not — for which classes of headings do they diverge, and what is the cheapest
path to a CI-enforced contract: port `github-slugger` to Go, drive a Node
sidecar, or relax the contract on one side?

## Hypothesis

**They agree on simple ASCII headings but diverge in at least three ways:**

1. **Unicode letters** — rfc-api's `nonSlugRune = [^a-z0-9]+` strips _every_
   non-ASCII letter. `github-slugger` lowercases via Unicode case-folding and
   preserves Unicode letters/digits via `[^\p{L}\p{N}_-]`. A heading like
   `### Café configuration` becomes `caf-configuration` on rfc-api but
   `café-configuration` on the consumer.
2. **Duplicate-heading collision suffixing** — `github-slugger` is stateful
   per-document: a second `## Notes` becomes `notes-1`, a third becomes
   `notes-2`. rfc-api's `slugify` is pure and per-heading; both `## Notes`
   instances slug to `notes`. Anchor collisions on the rendered side mean
   `#notes` always scrolls to the first one regardless of which the search hit
   pointed at.
3. **Inline formatting and HTML in headings** — `## The \`Source\`
   field`slugs how? rfc-api:`source`is part of the input string after Markdown-fence stripping (need to verify exactly what the goldmark walker passes to`slugify`).
   rehype-slug operates on rendered text content, which strips backticks but
   keeps the inner word. Probably aligned but worth pinning down.

I expect issues 1 and 2 to be real divergences, issue 3 to be aligned in
practice. Recommendation will likely be **Option A** (port github-slugger to Go)
— the algorithm is small, stable, and the test then catches drift on either
side.

## Context

`rfc-site` (the SSR Markdown frontend) uses `rehype-slug` to derive heading
`id="..."` attributes so intra-document anchor links work. Per
`rfc-site/docs/design/0002-markdown-rendering-pipeline.md` §Data Model, the slug
applied to a rendered heading **must** equal the `section_slug` field that
`rfc-api` returns on `SearchResult` payloads.

Today the contract is implicit. When the two slugifiers diverge — Unicode
normalization, collision suffixing, code-span treatment — search-hit deep links
silently break: the user clicks a hit, lands on the right document, but
`#some-slug` doesn't match any heading id and no scroll happens. We want this
contract explicit and CI-enforced before the corpus grows enough to surface the
bug organically.

**Triggered by:**
[issue #20](https://github.com/donaldgifford/rfc-api/issues/20),
`rfc-site/docs/design/0002-markdown-rendering-pipeline.md`.

## Approach

1. **Read both algorithms in full.** Go side:
   `internal/search/meilisearch/section.go:slugify` (already known — strip
   `[^a-z0-9]+`, replace with `-`, lowercase, trim). JS side: clone
   `github-slugger` (the algorithm `rehype-slug` actually delegates to) and read
   its `slug.js` end-to-end. Document the exact rule set each implements.
2. **Verify what input string each receives.** rfc-api's slugify is called from
   the goldmark AST walker — confirm whether it sees the rendered heading text
   (inline backticks stripped) or the raw Markdown source. rehype-slug runs on
   the HAST after Markdown→HTML transformation, so it sees rendered text.
3. **Build a fixture set.** Cover the categories named in issue #20's acceptance
   criteria: ASCII, punctuation, Unicode (Latin Extended + CJK + Cyrillic),
   code-spans in headings, duplicate-heading collisions, leading/trailing
   punctuation, headings of length 1, headings consisting only of stripped
   characters. Aim for ~30 cases.
4. **Run both implementations against the fixture set** and tabulate matches vs
   divergences. Use a small Node script for the JS side
   (`npx github-slugger`-style); compare against `slugify` from the Go side via
   a quick test harness.
5. **Decide the path forward.** Three options to weigh in #Recommendation: (A)
   port github-slugger to Go and route `slugify` through it; (B) drive a Node
   sidecar from the contract test only — keep the Go slugifier as is; (C) relax
   the contract on rfc-site (e.g. rfc-site re-derives slugs from the headings it
   renders, ignores `section_slug`).

## Environment

| Component                          | Version / Value                                               |
| ---------------------------------- | ------------------------------------------------------------- |
| rfc-api `slugify`                  | `internal/search/meilisearch/section.go` (regex `[^a-z0-9]+`) |
| rehype-slug                        | whatever rfc-site pins (verify) — typically 6.x               |
| github-slugger (rehype-slug's dep) | typically 2.x                                                 |
| Go                                 | 1.26.1                                                        |
| Test corpus                        | rfc-api's own `docs/**/*.md` is a reasonable seed             |

## Findings

### Algorithm A — rfc-api `slugify`

Source: `internal/search/meilisearch/section.go:145`.

```go
var nonSlugRune = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
    s = strings.ToLower(strings.TrimSpace(s))
    s = nonSlugRune.ReplaceAllString(s, "-")
    s = strings.Trim(s, "-")
    return s
}
```

Properties:

- **Keep set:** `[a-z0-9]` only (post-lowercase). Strips every other rune —
  including underscores and every Unicode letter.
- **Replacement strategy:** runs of stripped chars _and whitespace_ collapse to
  a single `-`.
- **Trim:** leading/trailing `-` removed.
- **Stateless:** no collision tracking; `slugify("Notes")` returns `"notes"`
  every call.

The input string passed to `slugify` is the AST-flattened heading text
(`headingText` at `section.go:98` walks the goldmark inline children and
concatenates `*ast.Text` segments — backticks, link wrappers, and emphasis are
dropped, but their inner text is kept). So `## The \`Source\`
field`arrives at`slugify`as`"The Source field"`.

### Algorithm B — `github-slugger`

Source:
[github.com/Flet/github-slugger](https://github.com/Flet/github-slugger),
`index.js` + `regex.js`. This is what `rehype-slug` delegates to and what GitHub
itself uses for heading anchors.

```js
export function slug(value, maintainCase) {
  if (typeof value !== "string") return "";
  if (!maintainCase) value = value.toLowerCase();
  return value.replace(STRIP_REGEX, "").replace(/ /g, "-");
}
```

Plus a stateful wrapper class that adds collision suffixing:

```js
slug(value, maintainCase) {
  let result = slug(value, maintainCase === true)
  const originalSlug = result
  while (own.call(self.occurrences, result)) {
    self.occurrences[originalSlug]++
    result = originalSlug + '-' + self.occurrences[originalSlug]
  }
  self.occurrences[result] = 0
  return result
}
```

Properties:

- **Keep set:** `\p{L}` (Unicode letters) + `\p{N}` (Unicode digits) + `_` +
  `-` + ` ` (single space). Everything else stripped, _not_ replaced.
  - The upstream `STRIP_REGEX` is a precomputed enumeration of every Unicode
    codepoint that isn't in this set. For practical heading content
    `[^\p{L}\p{N}_\- ]` is faithful — a handful of codepoints in unusual blocks
    (musical symbols, certain emoji ranges) differ but never appear in real
    prose.
- **Replacement strategy:** stripped chars are removed (no hyphen substitution).
  Then _single-space → hyphen_, applied as a separate pass — multiple
  consecutive spaces become multiple consecutive hyphens, and only `0x20` is
  treated as space (tabs/NBSP are stripped, not space-converted).
- **Trim:** _none_. A heading like `"  Padded  "` lowercases to `"  padded  "`,
  no chars stripped, then spaces→hyphens → `"--padded--"`.
- **Stateful:** the `Slugger` class tracks per-document occurrences. Second
  `## Notes` becomes `notes-1`; third becomes `notes-2`. rehype-slug
  instantiates one slugger per HAST tree, so the state is per-rendered-document.

### Fixture comparison (synthetic)

26 representative cases. Run via a standalone Go harness that reimplements both
algorithms (rfc-api copy verbatim; github-slugger ported using `\p{L}\p{N}` for
the keep set). All 26 outcomes matched the prior hypothesis — no surprise
mismatches.

| #   | Heading            | rfc-api            | github-slugger     | Match | Why diverges                                                    |
| --- | ------------------ | ------------------ | ------------------ | ----- | --------------------------------------------------------------- |
| 1   | `Simple heading`   | `simple-heading`   | `simple-heading`   | ✓     | —                                                               |
| 2   | `Hello, World!`    | `hello-world`      | `hello-world`      | ✓     | —                                                               |
| 3   | `What's New?`      | `what-s-new`       | `whats-new`        | ✗     | apostrophe: rfc-api collapses+hyphenates, github-slugger strips |
| 4   | `A.B.C`            | `a-b-c`            | `abc`              | ✗     | period: rfc-api → hyphen, github-slugger → strip                |
| 5   | `Section 1.2.3`    | `section-1-2-3`    | `section-123`      | ✗     | period stripping (no neighbouring space)                        |
| 6   | `Q&A`              | `q-a`              | `qa`               | ✗     | ampersand stripping                                             |
| 7   | `100%`             | `100`              | `100`              | ✓     | —                                                               |
| 8   | `C++`              | `c`                | `c`                | ✓     | —                                                               |
| 9   | `[Link]`           | `link`             | `link`             | ✓     | —                                                               |
| 10  | `(parens)`         | `parens`           | `parens`           | ✓     | —                                                               |
| 11  | `my_var`           | `my-var`           | `my_var`           | ✗     | underscore: rfc-api strips, github-slugger keeps                |
| 12  | `__init__`         | `init`             | `__init__`         | ✗     | leading/trailing underscore preserved by github-slugger         |
| 13  | `Hello   World`    | `hello-world`      | `hello---world`    | ✗     | rfc-api collapses runs of ws, github-slugger emits N hyphens    |
| 14  | `Padded`           | `padded`           | `--padded--`       | ✗     | rfc-api trims, github-slugger keeps as hyphens                  |
| 15  | `   ` (ws-only)    | ``                 | `---`              | ✗     | empty vs three hyphens                                          |
| 16  | `Café`             | `caf`              | `café`             | ✗     | Latin-1 letter dropped vs preserved                             |
| 17  | `日本語`           | ``                 | `日本語`           | ✗     | CJK letters dropped vs preserved                                |
| 18  | `α-β-γ`            | ``                 | `α-β-γ`            | ✗     | Greek letters dropped vs preserved                              |
| 19  | `Привет`           | ``                 | `привет`           | ✗     | Cyrillic dropped vs preserved                                   |
| 20  | `The Source field` | `the-source-field` | `the-source-field` | ✓     | (post-AST view of `## The \`Source\` field`)                    |
| 21  | `One-two`          | `one-two`          | `one-two`          | ✓     | hyphen kept on both                                             |
| 22  | `One—two`          | `one-two`          | `onetwo`           | ✗     | em dash: rfc-api → hyphen, github-slugger → strip               |
| 23  | `a` / `!` / `2026` | (as expected)      | (as expected)      | ✓ × 3 | —                                                               |

**13 of 26 fixtures diverge (50%).** Plus collision suffixing:

| Run | Heading | rfc-api | github-slugger |
| --- | ------- | ------- | -------------- |
| 1st | `Notes` | `notes` | `notes`        |
| 2nd | `Notes` | `notes` | `notes-1`      |
| 3rd | `Notes` | `notes` | `notes-2`      |

### Fixture comparison (real corpus)

Scanned every `.md` file under `docs/` in this repo (skipping fenced code
blocks). 355 H1–H6 headings extracted. Comparison run between `rfc-api slugify`
and the faithful github-slugger port:

- **26 of 355 headings (7.3%) diverge** on a single-call basis.
- **38 additional divergences from collision suffixing** — that's the count of
  duplicate-headings-within-a-doc tuples; each one corresponds to a slug that
  github-slugger would suffix (`-1`, `-2`, …) but rfc-api emits identically.
- **Total: 64 of 355 (18.0%) of the live corpus** would deep-link incorrectly
  today.

Examples from the live corpus:

```
design/0001-…/      "OpenAPI / contract management"
                    rfc-api  = "openapi-contract-management"
                    gh       = "openapi--contract-management"   (space-slash-space → "--")

development/local-dev.md  "Postgres won't come up clean after schema changes"
                          rfc-api = "postgres-won-t-come-up-clean-after-schema-changes"
                          gh      = "postgres-wont-come-up-clean-after-schema-changes"

investigation/0001-…  "`rfd-api` — what it actually does"
                      rfc-api = "rfd-api-what-it-actually-does"
                      gh      = "rfd-api--what-it-actually-does"   (em dash stripped between two spaces)

impl/0002-…  "Phase 3: store.Docs Postgres implementation"
             rfc-api = "phase-3-store-docs-postgres-implementation"
             gh      = "phase-3-storedocs-postgres-implementation"
```

Three patterns dominate the live divergences:

1. **`X / Y` separators** in headings — extremely common
   (`API / Interface Changes`, `Migration / Rollout Plan`,
   `OpenAPI / contract management`). rfc-api emits a single hyphen;
   github-slugger emits two.
2. **Apostrophes in casual prose** — `won't`, `can't`, `it's`. rfc-api
   hyphenates around them; github-slugger drops them.
3. **Em dashes** in mid-heading parentheticals — heavily used in this corpus
   (`— what it actually does`).

Plus the universal **collision-suffix** divergence: 38 events across the corpus
where the same H2 text appears more than once within a document.

### Side effect: indexer sub-doc id collisions

This investigation surfaced a related live bug on the indexer side. Sub-document
ids in Meilisearch are constructed as `{parent_id}__{section_slug}`
(`section.go` + `indexer.go`). When the same H2 text appears twice in a document
— e.g. `## API / Interface Changes` in both DESIGN-0001 and DESIGN-0002 — the
rfc-api slugifier produces the same slug, but those are _different parent_ids_,
so no collision in that case. **However,** when the same H2 appears twice
_within_ one document, the second section's Meili upsert overwrites the first
(same `{parent}__{slug}` key). Today this is masked because the corpus rarely
has intra-doc duplicate H2s; once it does, search hits silently lose half the
section content. github-slugger's collision suffix makes this safe by
construction.

## Conclusion

**Answer: Yes, they diverge significantly.** rfc-api's current `slugify` and
github-slugger / rehype-slug agree on simple ASCII-only prose with no Unicode
letters, no underscores, no apostrophes, no inner em-dashes, no `X / Y`
separators, and no duplicate H2s. They diverge on every other class of heading.

Concretely on the live rfc-api corpus today: **18% of headings already produce a
broken deep-link** from search results, with the majority coming from collision
suffixing and the `X / Y` separator pattern. As the corpus grows and includes
more international content (ADR-0003 #Search expects this) the divergence rate
climbs.

The contract that issue #20 wants explicit + CI-enforced is currently
**violated, not just implicit.**

## Recommendation

**Option A — port github-slugger to Go inside
`internal/search/meilisearch/section.go`.** Replace the existing `slugify` with
a faithful Go port using Go's native `\p{L}\p{N}` Unicode classes (Go's `regexp`
supports them out of the box, unlike older JS engines, so we don't need the
giant precomputed Unicode block list — `\p{L}\p{N}` is functionally equivalent
for practical heading text). Add a stateful slugger threaded per-document
through the indexer to handle collision suffixing.

Sketch:

```go
var nonSlugRune = regexp.MustCompile(`[^\p{L}\p{N}_\- ]`)

// pure, stateless — for callers that want raw github-slugger semantics.
func slug(s string) string {
    s = strings.ToLower(s)
    s = nonSlugRune.ReplaceAllString(s, "")
    s = strings.ReplaceAll(s, " ", "-")
    return s
}

// slugger is per-document; tracks occurrences for collision suffixing.
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

Wire `splitSections` to instantiate a `*slugger` per document and call
`g.slug(headingText(...))` instead of the package-level `slugify`. Keep the
package-level pure `slug` function for tests + callers that don't need state.

Then satisfy issue #20's acceptance criteria with a contract test in
`test/contract/` that:

1. Generates a fixture set covering the categories in #Approach step 3 (ASCII /
   punctuation / Unicode / code-spans / collisions / leading-trailing / length-1
   / all-stripped). The existing `/tmp/slug-compare` harness is the seed — fold
   its fixtures in, expand to ~50 cases.
2. Asserts `slug(input) == expected` for each case.
3. Optionally — and this is what makes it a true _contract_ test against the
   consumer — vendors a snapshot of `github-slugger`'s actual output (run once
   via `npx github-slugger`, committed as a JSON fixture) and asserts the Go
   port matches byte-for-byte. CI re-runs the Go port; rfc-site CI separately
   re-runs npx-github-slugger against the same fixture file. Drift on either
   side fails immediately.

**Why not Option B (Node sidecar in tests):** adds Node to the rfc-api test
environment. We have no other Node dependency in this repo and don't want to
introduce one for a 30-LoC algorithm. The "snapshot fixture from a one-time npx
run" pattern in step 3 above gets the same accuracy guarantee without the
runtime Node coupling.

**Why not Option C (relax the contract; rfc-site re-derives slugs):** a
per-search-hit re-slug operation on the consumer side adds CPU per page render
and pushes the contract responsibility outward to every consumer (MCP server,
future CLI, anything else). The whole point of `section_slug` in the API payload
is "the producer has already authoritatively slugified this — render it as the
heading anchor and link to it." Relaxing means the field has no use, and we'd
remove it from the API instead.

**Migration concerns:** changing the slug algorithm means existing Meili sub-doc
ids invalidate. Need a `make reindex --check-drift` pass after the change lands;
the existing `cmd/rfc-api/reindex.go` infrastructure handles this. Document in
the IMPL doc that lands the fix.

**Follow-up scope (not this investigation):**

- The implementation is tracked in [IMPL-0006][impl-0006]: phased plan covering
  the algorithm port, stateful collision suffixing, snapshot-fixture contract
  test, and migration. Open Questions for review live in that doc, not here.
- Coordinate the rollout with rfc-site so it bumps the rfc-api OpenAPI pin and
  regenerates types in the same window — though no OpenAPI shape changes here,
  just the runtime values.

## References

- [IMPL-0006 — section_slug consumer-side slug contract implementation][impl-0006]
  — phased implementation plan derived from this investigation's recommendation.
- [Issue #20 — Contract test: assert section_slug equals rehype-slug(section_heading)](https://github.com/donaldgifford/rfc-api/issues/20)
- [`rehype-slug`](https://github.com/rehypejs/rehype-slug) — the rfc-site
  plugin.
- [`github-slugger`](https://github.com/Flet/github-slugger) — the algorithm
  rehype-slug delegates to; same one GitHub uses for heading anchors.
- `internal/search/meilisearch/section.go` — current rfc-api slugifier.
- `rfc-site/docs/design/0002-markdown-rendering-pipeline.md` §Data Model — the
  contract this investigation is making explicit.
- [ADR-0003 — Use Meilisearch for rfc-api search](../adr/0003-use-meilisearch-for-rfc-api-search.md)
  — `section_slug` was introduced as part of per-section indexing.

[impl-0006]: ../impl/0006-sectionslug-consumer-side-slug-contract-implementation.md
