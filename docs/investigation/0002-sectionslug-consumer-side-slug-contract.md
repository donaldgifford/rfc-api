---
id: INV-0002
title: "section_slug consumer-side slug contract"
status: Open
author: Donald Gifford
created: 2026-05-08
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0002: section_slug consumer-side slug contract

**Status:** Open
**Author:** Donald Gifford
**Date:** 2026-05-08

<!--toc:start-->
- [Question](#question)
- [Hypothesis](#hypothesis)
- [Context](#context)
- [Approach](#approach)
- [Environment](#environment)
- [Findings](#findings)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

For every heading in the rfc-api corpus, does the `section_slug` value emitted by `internal/search/meilisearch/section.go:slugify` equal the slug that `rehype-slug` (via `github-slugger`) would produce for the same heading text?

If not — for which classes of headings do they diverge, and what is the cheapest path to a CI-enforced contract: port `github-slugger` to Go, drive a Node sidecar, or relax the contract on one side?

## Hypothesis

**They agree on simple ASCII headings but diverge in at least three ways:**

1. **Unicode letters** — rfc-api's `nonSlugRune = [^a-z0-9]+` strips *every* non-ASCII letter. `github-slugger` lowercases via Unicode case-folding and preserves Unicode letters/digits via `[^\p{L}\p{N}_-]`. A heading like `### Café configuration` becomes `caf-configuration` on rfc-api but `café-configuration` on the consumer.
2. **Duplicate-heading collision suffixing** — `github-slugger` is stateful per-document: a second `## Notes` becomes `notes-1`, a third becomes `notes-2`. rfc-api's `slugify` is pure and per-heading; both `## Notes` instances slug to `notes`. Anchor collisions on the rendered side mean `#notes` always scrolls to the first one regardless of which the search hit pointed at.
3. **Inline formatting and HTML in headings** — `## The \`Source\` field` slugs how? rfc-api: `source` is part of the input string after Markdown-fence stripping (need to verify exactly what the goldmark walker passes to `slugify`). rehype-slug operates on rendered text content, which strips backticks but keeps the inner word. Probably aligned but worth pinning down.

I expect issues 1 and 2 to be real divergences, issue 3 to be aligned in practice. Recommendation will likely be **Option A** (port github-slugger to Go) — the algorithm is small, stable, and the test then catches drift on either side.

## Context

`rfc-site` (the SSR Markdown frontend) uses `rehype-slug` to derive heading `id="..."` attributes so intra-document anchor links work. Per `rfc-site/docs/design/0002-markdown-rendering-pipeline.md` §Data Model, the slug applied to a rendered heading **must** equal the `section_slug` field that `rfc-api` returns on `SearchResult` payloads.

Today the contract is implicit. When the two slugifiers diverge — Unicode normalization, collision suffixing, code-span treatment — search-hit deep links silently break: the user clicks a hit, lands on the right document, but `#some-slug` doesn't match any heading id and no scroll happens. We want this contract explicit and CI-enforced before the corpus grows enough to surface the bug organically.

**Triggered by:** [issue #20](https://github.com/donaldgifford/rfc-api/issues/20), `rfc-site/docs/design/0002-markdown-rendering-pipeline.md`.

## Approach

1. **Read both algorithms in full.** Go side: `internal/search/meilisearch/section.go:slugify` (already known — strip `[^a-z0-9]+`, replace with `-`, lowercase, trim). JS side: clone `github-slugger` (the algorithm `rehype-slug` actually delegates to) and read its `slug.js` end-to-end. Document the exact rule set each implements.
2. **Verify what input string each receives.** rfc-api's slugify is called from the goldmark AST walker — confirm whether it sees the rendered heading text (inline backticks stripped) or the raw Markdown source. rehype-slug runs on the HAST after Markdown→HTML transformation, so it sees rendered text.
3. **Build a fixture set.** Cover the categories named in issue #20's acceptance criteria: ASCII, punctuation, Unicode (Latin Extended + CJK + Cyrillic), code-spans in headings, duplicate-heading collisions, leading/trailing punctuation, headings of length 1, headings consisting only of stripped characters. Aim for ~30 cases.
4. **Run both implementations against the fixture set** and tabulate matches vs divergences. Use a small Node script for the JS side (`npx github-slugger`-style); compare against `slugify` from the Go side via a quick test harness.
5. **Decide the path forward.** Three options to weigh in #Recommendation: (A) port github-slugger to Go and route `slugify` through it; (B) drive a Node sidecar from the contract test only — keep the Go slugifier as is; (C) relax the contract on rfc-site (e.g. rfc-site re-derives slugs from the headings it renders, ignores `section_slug`).

## Environment

| Component                         | Version / Value                                              |
|-----------------------------------|--------------------------------------------------------------|
| rfc-api `slugify`                 | `internal/search/meilisearch/section.go` (regex `[^a-z0-9]+`)|
| rehype-slug                       | whatever rfc-site pins (verify) — typically 6.x              |
| github-slugger (rehype-slug's dep)| typically 2.x                                                |
| Go                                | 1.26.1                                                       |
| Test corpus                       | rfc-api's own `docs/**/*.md` is a reasonable seed            |

## Findings

<!-- Filled in as the investigation progresses. -->

### Algorithm A — rfc-api `slugify`

```go
var nonSlugRune = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
    s = strings.ToLower(strings.TrimSpace(s))
    s = nonSlugRune.ReplaceAllString(s, "-")
    s = strings.Trim(s, "-")
    return s
}
```

Pure, stateless. ASCII-only post-strip. No collision handling.

### Algorithm B — `github-slugger`

<!-- TODO: read upstream slug.js, summarize the rule set:
     - case folding (lowercase via unicode-aware?)
     - allowed chars (\p{L}\p{N}_- per the upstream regex?)
     - separator (single hyphen?)
     - whitespace handling
     - trailing/leading punctuation
     - collision suffixing (per-instance state) -->

### Fixture comparison

<!-- TODO: table of (heading, rfc-api_slug, github-slugger_slug, match?). -->

## Conclusion

<!-- TODO: state the answer once findings are in. -->

**Answer:** TBD.

## Recommendation

<!-- TODO: pick A, B, or C from #Approach step 5 and justify. -->

## References

- [Issue #20 — Contract test: assert section_slug equals rehype-slug(section_heading)](https://github.com/donaldgifford/rfc-api/issues/20)
- [`rehype-slug`](https://github.com/rehypejs/rehype-slug) — the rfc-site plugin.
- [`github-slugger`](https://github.com/Flet/github-slugger) — the algorithm rehype-slug delegates to; same one GitHub uses for heading anchors.
- `internal/search/meilisearch/section.go` — current rfc-api slugifier.
- `rfc-site/docs/design/0002-markdown-rendering-pipeline.md` §Data Model — the contract this investigation is making explicit.
- [ADR-0003 — Use Meilisearch for rfc-api search](../adr/0003-use-meilisearch-for-rfc-api-search.md) — `section_slug` was introduced as part of per-section indexing.
