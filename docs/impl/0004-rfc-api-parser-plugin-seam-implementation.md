---
id: IMPL-0004
title: "rfc-api parser plugin seam implementation"
status: Draft
author: Donald Gifford
created: 2026-04-20
---
<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0004: rfc-api parser plugin seam implementation

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-04-20

<!--toc:start-->
- [Objective](#objective)
- [Scope](#scope)
  - [In Scope](#in-scope)
  - [Out of Scope](#out-of-scope)
- [Implementation Phases](#implementation-phases)
  - [Phase 1: Parser interface and registry](#phase-1-parser-interface-and-registry)
    - [Tasks](#tasks)
    - [Success Criteria](#success-criteria)
  - [Phase 2: docz-markdown parser](#phase-2-docz-markdown-parser)
    - [Tasks](#tasks-1)
    - [Success Criteria](#success-criteria-1)
  - [Phase 3: Link extraction](#phase-3-link-extraction)
    - [Tasks](#tasks-2)
    - [Success Criteria](#success-criteria-2)
  - [Phase 4: Validation, errors, and the "fake type" end-to-end test](#phase-4-validation-errors-and-the-fake-type-end-to-end-test)
    - [Tasks](#tasks-3)
    - [Success Criteria](#success-criteria-3)
- [File Changes](#file-changes)
- [Testing Plan](#testing-plan)
- [Dependencies](#dependencies)
- [Open Questions](#open-questions)
- [Resolved Decisions](#resolved-decisions)
- [References](#references)
<!--toc:end-->

## Objective

Concretize the `Parser` seam sketched in
[DESIGN-0002 #Parser plugin seam][design-0002-parser]. Ship a compile-time
parser registry and a `docz-markdown` implementation that covers RFC + ADR
frontmatter. This is the last blocker to the worker's ingest path in
[IMPL-0003][impl-0003].

DESIGN-0002 left this as an Open Question (compiled-in vs plugin vs
subprocess). This IMPL commits to **compiled-in for v1** with a clean
registration seam — the decision most aligned with Go idiom and small-
codebase pragmatism.

**Implements:** the parser seam in [DESIGN-0002][design-0002]; consumed by
[IMPL-0003][impl-0003] #Phase 4.

## Scope

### In Scope

- `Parser` interface in `internal/domain/parser.go`.
- Parser registry keyed by string name (`docz-markdown`, eventually
  `framework-markdown`, etc.) in `internal/parser/registry.go`.
- Concrete `docz-markdown` parser in `internal/parser/dozmarkdown/` that
  handles RFC + ADR frontmatter. Produces `domain.Document` with ID,
  Title, Status, Authors, CreatedAt, Body, Labels, Extensions.
- Markdown AST walking via `github.com/yuin/goldmark` for body-level
  link extraction.
- Frontmatter parsing via `gopkg.in/yaml.v3`.
- Lifecycle validation: Status ∈ `DocumentType.Lifecycle` (when the type
  declares one).
- Graduated "fake type" end-to-end test: a contrived `tst` type +
  minimal parser + the full `router_test.go` round-trip, hooked into
  the parser registry instead of hand-built fixtures.

### Out of Scope

- **Non-Markdown content types.** AsciiDoc, HTML, JSON Schema. Add a
  parser when a type needs one.
- **External / plugin-loaded parsers.** Compile-time only per OQ1.
- **Schema validation of `Extensions`.** DESIGN-0002 OQ2 defaults to
  open-ended; revisit if a consumer starts relying on specific fields.
- **Content fetching.** The worker (IMPL-0003) fetches; the parser
  receives the content bytes and acts pure.

## Implementation Phases

Each phase builds on the previous one. A phase is complete when all its tasks
are checked off and its success criteria are met.

---

### Phase 1: Parser interface and registry

Lock the seam so IMPL-0003 can wire it.

#### Tasks

- [ ] `internal/domain/parser.go`:
      ```go
      type Parser interface {
          // Parse a single file's contents into a Document.
          // t supplies type-specific knobs (Lifecycle validation,
          // Extensions schema hints). src is the repo + path + commit
          // pointer written to Document.Source. Implementations must be
          // safe for concurrent use — the worker calls Parse from its
          // ingest goroutines.
          Parse(raw []byte, t DocumentType, src Source) (Document, error)
      }
      ```
- [ ] `internal/parser/registry.go`: `Registry{byName map[string]domain.
      Parser}` with `Register(name, parser)` and `Get(name) (Parser, bool)`.
      Registration happens at package init time or via an explicit
      `Register()` call from `cmd/rfc-api/work.go`.
- [ ] `internal/domain/parser_errors.go`: sentinel errors for the parser
      seam, wrapping existing domain sentinels:
  - `ErrMalformedFrontmatter` → `domain.ErrInvalidInput`
  - `ErrUnknownStatus` → `domain.ErrInvalidInput`
  - `ErrParserNotRegistered` → `domain.ErrInvalidInput`
- [ ] Unit tests: register / lookup / double-register (error), unknown
      name, concurrent `Register` / `Get`.

#### Success Criteria

- `go doc internal/domain Parser` renders the contract cleanly.
- `Registry` handles two parsers in the same process without crossover.
- Every parser error path maps to a classified `httperr` status (proved
  by a table-driven test against `httperr.classify`).

---

### Phase 2: docz-markdown parser

The real parser for RFC + ADR frontmatter, matching the docz tooling we
already use in-repo.

#### Tasks

- [ ] `internal/parser/doczmarkdown/parser.go`: `Parser{}` implementing
      `domain.Parser`. Steps per Parse call:
  1. Split the document at the leading `---` delimiter; everything
     between the first pair is YAML frontmatter; everything after is
     Markdown body.
  2. Unmarshal the frontmatter into an internal `frontmatter` struct:
     `ID, Title, Status, Author, Created, Updated, Labels []string`,
     with an `Extensions map[string]any` catch-all for unknown keys
     (see OQ3 on the catch-all shape).
  3. Split `Author` on `,` — docz writes a comma-separated single
     string today; the Document model carries `[]Author`. Handle and
     email are optional and only populated if present in a richer
     `authors:` list.
  4. Populate `Document` fields; compute canonical `DocumentID` via
     `docid.Canonical(t.ID, numericPart(frontmatter.ID))`. The parser
     trusts but verifies — a mismatch between frontmatter prefix and
     `t.Prefix` is a hard error.
- [ ] Timestamps: prefer frontmatter `created`/`updated`; fall back to
      the commit timestamp on `Source.Commit` (the worker provides it).
- [ ] Status validation: if `t.Lifecycle` is non-empty, `fm.Status` must
      be one of its values; else accept any non-empty string.
- [ ] `package init()` registers the parser under name
      `docz-markdown` in the global registry (default access pattern).
- [ ] Golden-file tests: a handful of `testdata/*.md` inputs + expected
      `Document` JSON; drift detection via `go-cmp`.

#### Success Criteria

- Every doc in `docs/rfc/` and `docs/adr/` parses cleanly under this
  parser (not for production ingest — as a self-check).
- Malformed YAML returns `ErrMalformedFrontmatter` with the line number
  in the `Detail`.
- Missing required fields (`id`, `title`, `status`) produce
  `ErrInvalidInput` with a message naming the missing field.

---

### Phase 3: Link extraction

Outgoing cross-references from body prose, so
`/api/v1/{type}/{id}/links` has data.

#### Tasks

- [ ] Pick the recognition rule per OQ4 (default: match
      `[TEXT](PREFIX-NNNN)` or bare `PREFIX-NNNN` tokens where `PREFIX`
      is any registered type prefix).
- [ ] Walk the Markdown AST via `goldmark` (parse-time, not rendered),
      extract every such reference.
- [ ] Resolve to `domain.Link`: `{Direction: LinkOutgoing, Target:
      docid.Canonical(typeID, urlID), Label: linkText}`. Dedup by
      `(target, label)`.
- [ ] Incoming links (the reverse index) are computed server-side by
      the store on read, not stored redundantly. That's a store concern
      ([IMPL-0002][impl-0002]); the parser emits outgoing only.
- [ ] Unit tests: a body with a mix of Markdown links, bare references,
      and ambiguous shapes; assert the expected Link slice.

#### Success Criteria

- A hand-authored RFC body that references three existing RFCs produces
  three outgoing `Link` records, each resolvable via `docid.Parse`.
- Non-reference matches (e.g. an acronym that happens to look like
  `API-0001`) either don't match the regex or fall out of the registry
  lookup — documented either way.

---

### Phase 4: Validation, errors, and the "fake type" end-to-end test

Graduate the DESIGN-0002 "fake type" harness from the router-only test to
a real round-trip through the parser.

#### Tasks

- [ ] `internal/parser/testparser/parser.go`: a minimal parser used
      only in tests. Takes `raw []byte` as a YAML document, unmarshals
      into `domain.Document` directly — no Markdown, no link extraction.
- [ ] Register under name `test-parser`.
- [ ] `test/integration/faketype_test.go`: end-to-end test that
      registers a `tst` type with `test-parser`, seeds a couple of YAML
      fixtures via the in-memory queue, and asserts each sub-resource
      endpoint is wired and returns the expected shape.
- [ ] Lifecycle enforcement: a document with `status: Invalid` and a
      type that declares a lifecycle returns `ErrUnknownStatus`, mapped
      to 400 via `httperr`.

#### Success Criteria

- The graduated fake-type test passes against the real parser + router
  stack. (The router-only variant in `internal/server/router_test.go`
  still passes too; one is about route mounting, this one is about
  parse + persist.)
- Running the full `rfc-api` suite (`make ci && make test-integration`)
  is green.

---

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/domain/parser.go` | Create | `Parser` interface + error sentinels. |
| `internal/parser/registry.go` | Create | Name-keyed registry. |
| `internal/parser/doczmarkdown/parser.go` | Create | Concrete docz-markdown parser. |
| `internal/parser/doczmarkdown/testdata/` | Create | Golden fixtures. |
| `internal/parser/testparser/parser.go` | Create | Test-only parser. |
| `test/integration/faketype_test.go` | Create | End-to-end fake-type round trip. |
| `go.mod` | Modify | Add `goldmark`, `yaml.v3`, `go-cmp`. |
| `docs/design/0002-*.md` | Modify | Flip parser OQ1 to Resolved. |

## Testing Plan

- **Unit** — frontmatter happy path and every malformed-input branch
  (missing field, unknown status, prefix mismatch, YAML syntax error).
- **Golden files** — every doc in `docs/rfc/` + `docs/adr/` parses
  without error; `Document` JSON diffs against checked-in fixtures.
- **Integration** — fake-type round-trip via the parser registry +
  in-memory store.
- **Concurrency** — `go test -race` passes the parser suite with
  `go test -run=. -race -count=100`.

## Dependencies

- **DESIGN-0002** for the interface shape and the "compiled-in for v1"
  default.
- **IMPL-0001** for `docid` helpers (already shipped).
- **IMPL-0002** for where parsed documents land (depended on by the
  fake-type test).
- **IMPL-0003** is the caller — wiring lands with the worker.

New Go modules:

- `github.com/yuin/goldmark` — CommonMark + YAML-frontmatter support.
- `gopkg.in/yaml.v3` — frontmatter unmarshaling.
- `github.com/google/go-cmp` — golden-file diffs (test-only).

## Open Questions

None at this time. See [#Resolved Decisions](#resolved-decisions).

## Resolved Decisions

1. **Compiled-in parser registry, no plugin loader.** Go `plugin` is
   Linux/macOS-only and doesn't work with `goreleaser`'s distroless
   runtime image; subprocess parsers add operational surface for no v1
   win. A clean `Register()` seam keeps the door open for either later.
2. **Explicit registration from `cmd/rfc-api/work.go`.** Parsers do
   not self-register via `init()`. Centralized wiring means the active
   parser set is visible in one place, and tests can register a subset
   without mystery imports.
3. **`Extensions` represented as `map[string]any`.** Matches
   DESIGN-0002's default. If a consumer ever needs byte-for-byte
   round-tripping, swap to `json.RawMessage` then.
4. **Link recognition: Markdown-links + bare `PREFIX-NNNN` tokens.**
   Both heuristics because our existing docs use both. An explicit
   `links:` frontmatter field is additive if the heuristics get noisy.
5. **Parser splits the `author` string on commas for now.** Handles
   after `@` become `Author.Handle`. Brittle but works against the
   docz output we have today — flagged for a docz follow-up to emit
   a structured list instead.
6. **Lifecycle violations hard-fail the ingest.** A type that declares
   a `Lifecycle` rejects any non-matching status (`ErrUnknownStatus`
   → 400 via `httperr`). We'd rather operators fix the doc than serve
   one with an unrecognized status. Reversible if it bites.
7. **Commit-metadata author fallback belongs to the worker, not the
   parser.** When frontmatter lacks `author`, the worker fills from
   `co-authored-by` before handing the doc to the indexer; the parser
   stays pure (no Git, no I/O).
8. **No co-evolution with `docz` tooling.** `docz` owns authoring,
   this parser owns ingestion. Treat docz output as a format contract
   the parser consumes; changes to docz are handled by bumping the
   contract version, not by coupling the codebases.

## References

- [DESIGN-0002: DocumentType extensibility][design-0002]
- [DESIGN-0002 #Parser plugin seam][design-0002-parser]
- [IMPL-0001: HTTP server phase 1][impl-0001]
- [IMPL-0002: PostgreSQL store][impl-0002]
- [IMPL-0003: sync worker][impl-0003]
- [RFC-0001: rfc-api][rfc-0001]
- `goldmark`: <https://github.com/yuin/goldmark>

[design-0002]: ../design/0002-documenttype-extensibility-for-multiple-content-types.md
[design-0002-parser]: ../design/0002-documenttype-extensibility-for-multiple-content-types.md#parser-plugin-seam
[impl-0001]: ./0001-rfc-api-http-server-phase-1-implementation.md
[impl-0002]: ./0002-rfc-api-postgresql-store-implementation.md
[impl-0003]: ./0003-rfc-api-sync-worker-implementation.md
[rfc-0001]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
