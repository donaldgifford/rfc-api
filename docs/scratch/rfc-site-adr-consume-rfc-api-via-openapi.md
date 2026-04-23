---
id: ADR-0001
title: "Consume rfc-api via its published OpenAPI contract"
status: Proposed
created: 2026-04-23
# NOTE: This ADR is staged here temporarily. When the `rfc-site` repo is
# created, move this file to its `docs/adr/0001-*.md` and renumber if
# anything has landed there first. It is authored from rfc-site's
# perspective, not rfc-api's.
---

# ADR 0001: Consume rfc-api via its published OpenAPI contract

## Status

Proposed.

## Context

`rfc-site` is the SSR web frontend for the Markdown Portal ([RFC-0002][rfc-0002]).
Its sole runtime dependency is `rfc-api` — a Go service that serves Markdown
documents + cross-type search + discussion over HTTP. `rfc-api` is in a
separate repository ([github.com/donaldgifford/rfc-api][rfc-api-repo]), ships
independently (own semver, own Helm chart, own Deployment), and publishes a
hand-authored OpenAPI 3.1 spec as the source of truth for every consumer.

`rfc-site` needs to:
1. Fetch documents by type + id, including the raw Markdown body.
2. Paginate cross-type directory views and per-type lists.
3. Call cross-type search and render highlight snippets.
4. Fetch per-document sub-resources (authors, links, discussion).
5. Pass the user's bearer token through to `rfc-api` (post Phase 4).
6. Do all of the above without touching Postgres, Meilisearch, or GitHub
   directly.

The question is how tightly to couple the frontend client code to the backend
shape.

## Decision

**`rfc-site` consumes `rfc-api` exclusively through the `api/openapi.yaml`
contract published by the `rfc-api` repo.** Concretely:

1. **Vendor the spec.** Copy `api/openapi.yaml` from a pinned `rfc-api` release
   tag into `rfc-site` at build time (or a submodule / flake input — pick one in
   Phase 1). Do *not* hand-translate types.
2. **Generate a typed TS client** from the vendored spec using `openapi-ts` (or
   equivalent). The generator runs as part of `rfc-site`'s build; the generated
   output is gitignored and regenerated on CI + locally. Upgrading the pinned
   `rfc-api` version is a one-line bump + regenerate + commit.
3. **No hand-written request/response types** for anything `rfc-api` owns.
   Extending the contract — new endpoint, new field — is an `rfc-api` PR, not
   an `rfc-site` PR.
4. **Contract drift is a CI failure.** `rfc-site` CI runs the generator on
   every push and fails if the generated output diverges from the committed
   types stub (or, if a test build is cheap, from the last successful build).
   A spec change in `rfc-api` shouldn't silently land in a `rfc-site` release
   without a visible PR diff.

### What rfc-site needs to know about the shape

| Concern | Shape |
|--------|-------|
| **Base URL** | Injected at deploy time (`RFC_API_URL` or similar). Main port only — `/api/v1/*`. Never call `:8081/*` (admin port, ops-only). |
| **Versioning** | All paths prefixed `/api/v1/`. A breaking change is `/api/v2/` and a parallel surface; `v1` stays live through deprecation. |
| **Document id in URLs** | Numeric. `/api/v1/rfc/1` not `/api/v1/rfc/RFC-0001`. |
| **Canonical display id** | `{PREFIX}-{zero-padded-4}`, e.g. `RFC-0001`, `ADR-0042`. Appears in payloads as `id` + `display_id`. |
| **List endpoints** | Bare JSON arrays (never `null`). Total in `X-Total-Count`; next/prev cursors in RFC 8288 `Link` header. Cursor is an opaque base64 string — treat as a blob, don't parse it. |
| **Body field** | `body` is raw Markdown. `rfc-site` server-renders it. `rfc-api` never returns HTML. |
| **Error envelope** | `application/problem+json` (RFC 7807). Shape: `{type, title, status, detail, instance}`. Human-readable detail; UI can show it verbatim or map by `title`. |
| **Sub-resources** | `/api/v1/{type}/{id}/authors`, `/links`, `/discussion`. Each returns the shape declared in the spec — don't assume they're cached joins of the parent. |
| **Search** | `GET /api/v1/search?q=...&type=...&limit=...&cursor=...`. Response includes highlight snippets + `matched_terms`. Cursor is opaque (internally base64 offset; may change — don't rely on it). |
| **Webhook endpoint** | `POST /api/v1/webhooks/github` — for GitHub only. `rfc-site` never calls it. |
| **CORS** | `rfc-api` enforces an allow-list via `RFC_API_CORS_ORIGINS`. SSR requests from the `rfc-site` server don't need CORS (server-to-server), but any browser-side direct call (future admin UI, preview widget, etc.) requires `rfc-site`'s public origin to be added to that env var in both dev and prod. |
| **Auth (Phase 1–3)** | None. Internal network only. No `Authorization` header needed. |
| **Auth (Phase 4, OIDC)** | `rfc-site` completes the OIDC authz-code flow against Keycloak/Okta and forwards the access token to `rfc-api` as `Authorization: Bearer <jwt>`. `rfc-api` validates the JWT against the IdP's JWKS. No session sharing beyond the bearer. |
| **Tracing** | `rfc-api` accepts W3C `traceparent` headers on every request and joins the trace. `rfc-site` should propagate its SSR span. |

### Non-decisions (deferred)

- **OpenAPI generator choice.** `openapi-ts`, `orval`, `openapi-typescript-codegen`,
  framework-specific (e.g. `sveltekit-openapi`) — all valid. Pick in Phase 1
  after the framework ADR.
- **Caching strategy.** Per-pod in-memory vs Redis vs plain HTTP caching. RFC-0002
  Open Question #5.
- **Vendoring mechanism.** Git submodule vs scripted copy-on-tag vs `npm`-packaged
  spec. Pick in Phase 1; all reversible.

## Consequences

**Positive**

- Breaking-change visibility: a field rename or param deletion surfaces as a
  generated-type diff in a `rfc-site` PR, not as a runtime 400.
- Single source of truth: MCP clients, `rfc-site`, and any future consumer all
  negotiate the same surface. Departures from the spec are bugs in `rfc-api`.
- Parallel development: frontend contributors can stub against the spec before
  the matching endpoint ships, and `rfc-api` contract tests
  (`rfc-api/test/contract/`) already prove the server matches the spec.

**Negative**

- `rfc-site` is coupled to `rfc-api`'s release cadence for new fields. If the
  frontend needs a new field, it's an `rfc-api` PR first, then a `rfc-site` bump.
  Acceptable: this is the point of "one canonical surface."
- Generator output is large and regenerates on every dep bump. Mitigation:
  gitignore the output, commit a small types stub for IDE ergonomics, let CI
  catch drift.

**Neutral**

- If the spec is wrong (missing an endpoint the server actually serves, or vice
  versa), `rfc-api`'s contract tests will catch it before merge. `rfc-site`
  inherits that guarantee for free.

## Related decisions

- [RFC-0002][rfc-0002] — `rfc-site` scope + phases.
- [ADR-0004 (to be written)][adr-0004] — framework pick (SvelteKit / Next.js /
  Remix RR7 / Astro). Drives the generator choice.
- [rfc-api DESIGN-0001][rfc-api-design-0001] — HTTP server structure, middleware
  chain, error envelope rationale.
- [rfc-api DESIGN-0002][rfc-api-design-0002] — `DocumentType` extensibility;
  the "type is a parameter, not a package name" rule that makes `/api/v1/{type}`
  work.

[rfc-0002]: ../rfc/0002-rfc-site-web-frontend-for-the-markdown-portal.md
[rfc-api-repo]: https://github.com/donaldgifford/rfc-api
[rfc-api-design-0001]: https://github.com/donaldgifford/rfc-api/blob/main/docs/design/0001-rfc-api-http-server-go-net-http-structure.md
[rfc-api-design-0002]: https://github.com/donaldgifford/rfc-api/blob/main/docs/design/0002-documenttype-extensibility-for-multiple-content-types.md
[adr-0004]: ./0004-framework-pick.md
