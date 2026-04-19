---
id: 0011
title: Markdown Portal
status: Draft
author: donald
created: 2026-04-18
labels: [platform, docs, tooling]
---

# RFC-0011: Markdown Portal

## Summary

A web portal that renders Markdown documents stored in GitHub repositories, with
an attached read API and MCP server for programmatic and agentic consumption.
The portal is a generic surface over Git-managed Markdown — RFCs and ADRs are
the v1 content types, and other types (security frameworks, internal guidelines,
runbooks) can be added later without structural changes.

The portal is the _viewer and the API_. It does not own the authoring workflow.
Authorship and review continue to happen in Git via pull requests, following
whatever conventions the source repo adopts.

## Motivation

We want to adopt a Git-based, PR-driven workflow for RFCs in the style of
Oxide's RFD process. That workflow depends on GitHub — branch per document, PR
for discussion, merge-to-`main` to publish. GitHub handles authoring, review,
and state transitions just fine. What GitHub does _not_ provide:

- A readable rendering of our documents for non-developer audiences.
- Search across a corpus of documents.
- Programmatic access by tools and agents (LLM assistants, automation).
- Cross-document linking with context.
- A consistent experience across different content types in different repos.

Oxide solved this for themselves with [rfd.shared.oxide.computer][rfd-site] and
the [`rfd-site`][rfd-repo] project. Their approach validates the shape: a
frontend + API backed by Git as the source of truth. Their site is
AsciiDoc-specific and tightly coupled to their design system; ours will use
Markdown and be usable for more than just RFCs.

The tooling Oxide describes in [RFD 1 §Tooling][rfd-tooling] is a good reference
for the kind of surface we're building — the web reading experience, the search,
the inter-document links. We want that experience, against our conventions, with
an API and MCP layer added for programmatic and agentic use.

[rfd-site]: https://rfd.shared.oxide.computer/
[rfd-repo]: https://github.com/oxidecomputer/rfd-site
[rfd-tooling]: https://rfd.shared.oxide.computer/rfd/0001#_tooling

## Goals

- **Render Markdown documents from a GitHub repo as a readable website**, with
  navigation, typography, and cross-document linking appropriate for a document
  corpus.
- **Support multiple content types from day one.** v1 supports RFCs and ADRs,
  which share structure but differ in frontmatter and lifecycle. The design must
  generalize — an RFC and an ADR are both "Markdown documents in a Git repo with
  some frontmatter," and future types (frameworks, guidelines, runbooks) should
  slot in without core changes.
- **Full-text search** across the corpus with a keyboard-first UX.
- **Read API** that exposes documents, metadata, and search as JSON for tools,
  automation, and cross-referencing from other systems.
- **MCP server** so LLM-based agents can list, fetch, and search documents as
  tools.
- **Live sync from Git** so published content in the portal reflects the repo
  within minutes of a merge.
- **Deployable on our existing Kubernetes infrastructure** without exotic
  dependencies.

## Non-Goals

- **Authoring in the browser.** Writing, editing, and reviewing happen in Git.
  The portal is strictly read-only.
- **Replacing GitHub's PR discussion.** Comments, approvals, and merge workflows
  stay on GitHub. The portal may surface or deep-link to PR content in future
  phases, but it will not reimplement it.
- **Being a CMS.** No admin UI for editing metadata or content outside Git.
- **Prescribing the RFC process itself.** The portal renders whatever the source
  repo contains. How teams structure their documents, what statuses they use,
  and what their review process looks like are owned by the source repo's
  conventions, not the portal.
- **Defining the design system.** The portal is a consumer of a design system,
  not the design system itself. That is RFC-0012.
- **Authentication for reading in v1.** Internal-network only at first; SSO is a
  future phase.

## Proposed Solution

At the highest level:

```
┌──────────────┐        ┌───────────────┐        ┌───────────────┐
│  GitHub repo │──sync─▶│   Portal API  │◀───────│   Portal web  │
│  (Markdown)  │        │  + search idx │        │    frontend   │
└──────────────┘        └───────┬───────┘        └───────────────┘
                                │
                                └─────◀─── MCP server (tools)
```

### Content model

A **content source** is a GitHub repo (or a path within one) plus a parser that
turns files in that source into **documents**. A document is the portal's unit
of rendering: an ID, a title, a status, a body, a set of labels, a set of links
to other documents, and arbitrary frontmatter.

Different content types bring different parsers. RFCs expect `docz`-style
frontmatter. A future framework content type would expect a different schema.
The portal's core doesn't care — it treats documents uniformly once parsed.

Multiple content sources can feed the same portal instance. Sources are
configured statically (at portal deploy time) to start.

### Sync

The portal reconciles against its configured sources on a timer. Every
configured repo is scanned periodically, changes are parsed, and the portal's
derived store is updated. GitHub webhooks can trigger the same reconcile out of
schedule for low-latency updates.

The repo is the source of truth. The portal's store is rebuildable from it at
any time.

### Read API

A JSON HTTP API exposing documents, lists, search, and metadata. Typical
endpoints:

```
GET  /api/v1/docs                    list with filters
GET  /api/v1/docs/{id}               single document
GET  /api/v1/docs/{id}/links         cross-references
GET  /api/v1/search?q=...            full-text search
GET  /api/v1/sources                 configured content sources
GET  /healthz
```

The API is rate-limited and initially open (internal-network only). It is the
canonical programmatic surface — the frontend and MCP both consume it.

### MCP server

A Model Context Protocol server that exposes the read API as LLM tools. Allows
agents in Claude Code, Cursor, or any MCP-aware client to search, fetch, and
reference documents directly without browsing the web UI.

The MCP server is a thin adapter over the HTTP API. It is versioned
independently and distributed as a standalone binary.

### Web frontend

A single-page or server-rendered web app that presents the content as a readable
site — directory, single-document view, search, and content-type- specific views
as they are added. The frontend consumes the API; it has no direct access to the
repo or the store.

Visual design and component system are covered by RFC-0012.

### Deployment

Packaged as a single Helm chart deployed to our existing Kubernetes cluster via
Argo, alongside whatever datastore and search backend the implementation design
doc settles on.

## Alternatives Considered

1. **Fork `rfd-site`.** Rejected. Their site is tightly coupled to AsciiDoc,
   their design system, and a Rust backend we would need to rewrite anyway to
   swap the document format. We are borrowing the architectural shape, not the
   codebase.
2. **Static site generator (MkDocs, Docusaurus, Hugo) + GitHub Actions.**
   Rejected. No dynamic API, no cross-source search, no MCP surface. Works for a
   single corpus read by humans; does not satisfy the API/MCP goal.
3. **Backstage TechDocs.** Wrong shape. TechDocs is component-scoped, designed
   to attach docs to services in a catalog. Our corpus is flat and
   cross-cutting.
4. **GitHub's native rendering only.** Satisfies the "render Markdown" goal
   minimally. No search across repos, no API, no MCP, no custom per-content-
   type views. Good as a fallback; not sufficient as our primary surface.
5. **Write our own authoring experience in the browser.** Out of scope for this
   RFC. The Git-based workflow is intentional — it means we inherit GitHub's
   review tooling rather than rebuilding it.

## Drawbacks

- **Another service to operate.** Frontend, API, datastore, search, MCP server —
  five moving parts on our cluster, even if some are trivially small. Bus factor
  and maintenance cost apply.
- **Lock-in to GitHub.** If we ever move off GitHub for source hosting, the sync
  layer has to be rewritten. We judge that risk acceptable.
- **Duplication of truth.** The portal's store is a cache; if it drifts from the
  repo, users see stale or wrong content. Reconcile-on-a-timer mitigates but
  does not eliminate this.
- **Initial investment vs. GitHub-only.** A team that just wants to read
  Markdown on GitHub gets no marginal value from the portal until there are
  enough documents, content types, or automation needs to justify it.

## Rollout Plan

Phased, each phase independently valuable:

1. **Read-only portal for RFCs and ADRs.** Directory, single-document view,
   search, basic API. Internal-network only. No MCP yet.
2. **MCP server + API polish.** Expose the content as tools; iterate on API
   ergonomics based on how the MCP is used in practice.
3. **Third content type.** Validate that the "generic document" abstraction
   holds by adding a type with different shape — candidate: security frameworks
   generated from our HCL tooling. Adjust the core if needed.
4. **Stretch features.** Inline PR discussion overlay, per-type
   changelog/activity feed, and other deeper GitHub integrations as demand
   appears.
5. **Auth.** SSO on the frontend when we need it. API token auth for
   higher-rate-limit or write-adjacent use cases.

Each phase produces a shippable portal. The decision to proceed to the next
phase is informed by usage of the current one.

Each phase produces a shippable portal. The decision to proceed to the next
phase is informed by usage of the current one.

## Open Questions

1. **Content types in v1.** v1 supports **RFCs** (proposals requiring discussion
   and sign-off) and **ADRs** (records of specific technology or approach
   decisions that apply to one or more RFCs, or stand alone — e.g. "we use Go
   for these kinds of services, because of X, and we structure it this way").
   Both share the same underlying shape (Markdown + frontmatter in Git) and
   rendering surface; they differ in their frontmatter fields and lifecycle
   states. Future types (frameworks, runbooks, etc.) are anticipated but not in
   v1.
2. **PR discussion integration.** Two stretch goals worth flagging but not
   committing to for v1:
   - **Inline revisions and PR comments.** Oxide surfaces the PR discussion
     inline with the rendered document, mapping comments back to the lines they
     were left on. Valuable for understanding how a document evolved. Dependency
     on GitHub API rate limits and mapping stability.
   - **Per-type changelog / activity feed.** A rolling view of recent activity
     across documents of a given type — "what's new in RFCs this week," "recent
     ADRs." Useful for discovery. Both are explicit non-goals for v1 but called
     out so the data model and ingest pipeline don't foreclose them.
3. **`docz` as convention and future tooling.** `docz` provides the template
   shapes and frontmatter conventions we use. v1 treats `docz` as the source of
   conventions only — the portal parses whatever the repo contains and does not
   depend on `docz` tooling at runtime. As we gain experience with real authors
   and real documents, `docz` is a likely home for author-facing helpers
   (new-document generators, lint-on-commit, etc.). Frontmatter or template
   details will evolve; v1 does not try to lock them down.
4. **Source of truth vs. generated content.** Some content types — like security
   frameworks — are expected to be _generated_ by other tooling (e.g., a CLI
   that renders an HCL config both into Wiz API calls and into Markdown docs for
   this portal). From the portal's perspective, the Git repo remains the source
   of truth; how files arrive there (hand-authored, tool-generated,
   CI-committed) is out of scope. The ingest pipeline treats all sources
   identically.

## Security

- v1 is internal-network only; no exposure beyond the cluster.
- The portal's store is a derived cache. Loss of the store is recoverable from
  Git.
- Rendered Markdown is sanitized. The portal does not execute user-supplied
  scripts, nor does it follow arbitrary URLs at render time.
- Source repos are configured statically; the portal does not fetch arbitrary
  Git URLs.

## Prior Art

- Oxide's [RFD site][rfd-site] and [`rfd-site`][rfd-repo] / `rfd-api` repos.
  Architectural shape (viewer + API + Git-as-source-of-truth) is directly
  borrowed; implementation is not.
- [RFD 1][rfd-1], particularly §Tooling, for how the read experience, search,
  and inter-document linking are framed.
- Rust RFC process, Go proposal process, Joyent RFDs — general RFC lineage,
  mostly informational.

[rfd-1]: https://rfd.shared.oxide.computer/rfd/0001
