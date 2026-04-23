---
title: "rfc-site integration reference"
created: 2026-04-23
# NOTE: Staged here temporarily. When the `rfc-site` repo is created,
# move this file to its `docs/` directory. Intended audience: a frontend
# developer integrating rfc-site against rfc-api for the first time.
---

# rfc-site integration reference

Companion to [ADR-0001 (staged)](./rfc-site-adr-consume-rfc-api-via-openapi.md).
The ADR records *why* rfc-site consumes `rfc-api` via its OpenAPI contract;
this doc is the *how* — sample payloads, error-sentinel mapping, the Markdown
contract, and how to run the stack locally.

The authoritative spec is `api/openapi.yaml` in the
[rfc-api repo][rfc-api-repo]. When the spec and this doc disagree, the spec
wins — this doc is a cookbook, not a spec.

## Endpoint cookbook

### `GET /api/v1/types` — registered document types

```sh
curl -s http://localhost:8080/api/v1/types | jq
```

```json
[
  {
    "id": "rfc",
    "display_prefix": "RFC",
    "title": "Request for Comments",
    "statuses": ["Draft", "Proposed", "Accepted", "Rejected", "Superseded"]
  },
  {
    "id": "adr",
    "display_prefix": "ADR",
    "title": "Architecture Decision Record",
    "statuses": ["Proposed", "Accepted", "Deprecated", "Superseded"]
  }
]
```

Use this once at boot (or per-request with a cache) to drive the navigation
tabs and to validate user-supplied type filters before hitting downstream
endpoints.

### `GET /api/v1/docs` — cross-type list

```sh
curl -sD - "http://localhost:8080/api/v1/docs?limit=50" | jq
```

Headers of interest:

```
HTTP/1.1 200 OK
Content-Type: application/json
X-Total-Count: 42
Link: </api/v1/docs?cursor=b2ZmOjUw&limit=50>; rel="next"
```

Body is a **bare JSON array** (never `null`). Sorted `(created_at DESC, id ASC)`.

### `GET /api/v1/search?q=...` — cross-type full-text search

```sh
curl -s "http://localhost:8080/api/v1/search?q=postgres&limit=10" | jq
```

```json
[
  {
    "document": {
      "id": "IMPL-0002",
      "type": "impl",
      "title": "PostgreSQL store implementation",
      "status": "Completed",
      "created_at": "2026-04-18T12:34:56Z",
      "updated_at": "2026-04-19T09:00:00Z",
      "source": { "repo": "donaldgifford/rfc-api", "path": "docs/impl/0002-...md" }
    },
    "score": 0.94,
    "snippet": "...keyset pagination against <em>postgres</em> with ...",
    "matched_terms": ["postgres"],
    "section_heading": "Keyset pagination",
    "section_slug": "keyset-pagination"
  }
]
```

Pagination: `?cursor=<opaque>&limit=50`. Treat `cursor` as a blob — internally
it's `base64("off:N")` today but may change. Always read it from the previous
response's `Link` header.

Rendering notes:
- `snippet` contains `<em>...</em>` highlight tags. SSR can pass it to the
  Markdown renderer inside a `<mark>`/`<em>`-safe wrapper, or regex-replace
  if you don't trust upstream.
- `matched_terms` is provided for clients that *can't* render HTML (MCP,
  CLI). rfc-site will usually use `snippet`.
- `section_heading` + `section_slug` are set when the hit matches a
  sub-section (H1/H2). Use the slug to deep-link: `/rfc/1#keyset-pagination`.
  Empty when the hit is on the doc head.

### `GET /api/v1/{type}` — single-type list

Same shape as `/api/v1/docs` but filtered to one type. Use for the per-type
index page.

### `GET /api/v1/{type}/{id}` — single document

**ID in URL is numeric, zero-padding optional.** Canonical display id
(`RFC-0001`) appears in the payload's `id` field.

```sh
curl -s http://localhost:8080/api/v1/rfc/1 | jq
```

```json
{
  "id": "RFC-0001",
  "type": "rfc",
  "title": "rfc-api: Backend API for the Markdown Portal",
  "status": "Accepted",
  "authors": [{ "name": "Donald Gifford", "handle": "donaldgifford" }],
  "created_at": "2026-04-18T00:00:00Z",
  "updated_at": "2026-04-21T00:00:00Z",
  "body": "# RFC 0001: rfc-api...\n\n## Summary\n\n...",
  "links": [
    { "direction": "outgoing", "target": "ADR-0001", "href": "/api/v1/adr/1", "label": "Use Go..." }
  ],
  "labels": [],
  "extensions": { "phase": "1" },
  "source": {
    "repo": "donaldgifford/rfc-api",
    "path": "docs/rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md",
    "commit": "abc123..."
  }
}
```

`body` is **raw Markdown**. rfc-site server-renders it (see
[#Markdown contract](#markdown-contract) below).

### Sub-resources

All return 404 if the parent doc doesn't exist; otherwise return `[]` (never
`null`) or an empty object for `discussion`.

- `GET /api/v1/{type}/{id}/authors` → `[{name, email?, handle?}]`
- `GET /api/v1/{type}/{id}/links` → `[{direction, target, href, label?}]`
- `GET /api/v1/{type}/{id}/discussion` → `{url?, comment_count?, participants?, last_activity?}`

These are **not** guaranteed to be the same objects embedded in the parent
document payload — they may query different tables and surface fresher data
(especially `discussion`, which refreshes on GitHub webhooks). Prefer
sub-resources when the UI wants live-ish data; rely on the embedded versions
for initial SSR.

### `GET /api/v1/{type}/{id}/revisions`

Phase 2 stub — returns `[]` today. Don't design UI around it yet.

### Admin port (never call from rfc-site)

`:8081/healthz`, `:8081/readyz`, `:8081/metrics` are for Kubernetes / Prometheus
only. Network-policy-gated in prod. If rfc-site needs a "is the backend up"
indicator, implement a server-side health endpoint on rfc-site that synthesizes
it from `/api/v1/types` success.

## Error contract

Every 4xx/5xx is `Content-Type: application/problem+json` (RFC 7807):

```json
{
  "type": "/problems/not-found",
  "title": "Resource not found",
  "status": 404,
  "detail": "rfc RFC-9999 not found",
  "instance": "/api/v1/rfc/9999",
  "request_id": "01HTZ..."
}
```

### Sentinel → HTTP status → suggested UI behavior

| Server sentinel | Status | `type` URI | UI behavior |
|---|---|---|---|
| `ErrNotFound` | 404 | `/problems/not-found` | Show a 404 page that links to the directory. Do not retry. |
| `ErrInvalidInput` | 400 | `/problems/invalid-input` | Likely a bad query string from client code — log + show generic "bad request" page. Usually a bug in rfc-site. |
| `ErrConflict` | 409 | `/problems/conflict` | rfc-site doesn't mutate, so this shouldn't appear on the read path. If it does, treat like 500. |
| `ErrUpstream` | 502 | `/problems/upstream` | GitHub / Meili / Postgres hiccup. Show "search is temporarily unavailable" for search hits; show cached/stale doc body for doc fetches if possible. Retry once with backoff. |
| `ErrUnauthenticated` | 401 | `/problems/unauthenticated` | **Phase 4 only.** Redirect to the OIDC login flow; include the current path as a return-to. |
| `ErrRateLimited` | 429 | `/problems/rate-limited` | Read `Retry-After` header (seconds). Show a soft error; retry after the window. |
| *(default)* | 500 | `/problems/internal` | Generic error page. `detail` is a fixed string — don't display it verbatim, it's intentionally opaque. The real error is server-logged under `request_id`. |

Always surface `request_id` in the error UI (small, bottom of the page, or a
"report this" modal). It's how ops correlates logs to the user-reported issue.

## Markdown contract

`body` is Markdown authored via [docz](https://github.com/donaldgifford/docz).
The parser strips YAML frontmatter before persisting, so rfc-site receives the
**body only** — no frontmatter.

What to expect in the body:

- **GitHub-Flavored Markdown** is the baseline: headings, lists, tables, task
  lists, fenced code blocks, backtick-inline-code, reference-style links.
- **Code fences with language hints** (```go, ```sh, ```json, etc.). Plan for
  a syntax highlighter; `shiki` or `rehype-highlight` is fine.
- **Mermaid diagrams** in fenced `mermaid` blocks. Render client-side with the
  Mermaid library, or SSR via `@mermaid-js/mermaid-cli` if TTFB matters more.
- **Admonition-ish blockquote patterns** (`> **Note:** ...`) are in use but not
  syntactically special. A future ADR may formalize.
- **Relative links** between documents (e.g. `[DESIGN-0001](./0001-...md)`).
  The `links` array on the doc resolves these to canonical targets (`DESIGN-0001`)
  — prefer rendering from `links` over parsing the Markdown yourself.
- **Comments** of the form `<!-- toc:start --> ... <!-- toc:end -->` and
  `<!-- markdownlint-disable-file MD025 MD041 -->` may appear near the top.
  Strip them before rendering (trivially via a remark plugin).

What **not** to expect:

- No raw HTML passthrough. Sanitize just in case (XSS via future author
  carelessness).
- No JSX / MDX. The backend parses pure Markdown.
- No embedded scripts, iframes, or style tags.

Sanitization: run the rendered HTML through `rehype-sanitize` with the GFM
allowlist plus `mermaid` / `pre` / `code` / `em` (for search highlights). Do
not pass through `<script>` or `<iframe>` under any circumstance.

## Local development

### rfc-api dev loop (reference)

In a terminal in the `rfc-api` checkout:

```sh
mise install
cp .env.example .env
make compose-up                 # Postgres + Meilisearch
go run ./cmd/rfc-api serve      # main :8080, admin :8081
# Optional second terminal, for content ingest:
GITHUB_TOKEN=... go run ./cmd/rfc-api work
```

Now `http://localhost:8080/api/v1/types` answers on the host.

### Pointing rfc-site at rfc-api

rfc-site reads its backend URL from an env var. Convention: `RFC_API_URL`
(name TBD in the rfc-site framework ADR).

```sh
# rfc-site/.env.local
RFC_API_URL=http://localhost:8080
```

For SSR requests (server-to-server), no CORS or auth is needed. For any
browser-side fetches — e.g. a client-rendered search overlay, admin toggles
— the browser origin must be on rfc-api's CORS allow-list:

```sh
# rfc-api/.env
RFC_API_CORS_ORIGINS=http://localhost:5173,https://docs.yourdomain.tld
```

(Comma-separated, exact origin match. Empty value = deny all browser origins.)

### Seeding a dev corpus

With no content repo configured, rfc-api serves an empty set. Two options:

1. **Point the worker at a real GitHub repo** — set `RFC_API_WORKER_SOURCE_REPOS`
   per `rfc-api/.env.example` and set `GITHUB_TOKEN`. The worker scanner + ingest
   pipeline populates Postgres within one tick.
2. **Docs-site dogfooding** — point it at this repo (`donaldgifford/rfc-api`)
   itself; `docs/{rfc,adr,design,impl,investigation}/*.md` forms a natural
   corpus.

### Tracing across the seam

rfc-site should propagate a W3C `traceparent` header on every request to
rfc-api; rfc-api joins the trace automatically (otelhttp is the outermost
middleware). With the `obs` compose profile (`make compose-up-obs` in rfc-api),
spans from both services land in the same Jaeger trace.

## References

- [ADR-0001 (staged)](./rfc-site-adr-consume-rfc-api-via-openapi.md) — the
  integration decision.
- [`api/openapi.yaml`][rfc-api-openapi] — authoritative spec.
- [rfc-api DESIGN-0001][rfc-api-design-0001] — HTTP server structure (error
  envelope, middleware order, CORS).
- [rfc-api DESIGN-0002][rfc-api-design-0002] — DocumentType extensibility.
- [rfc-api `docs/development/`][rfc-api-dev] — dev-loop runbook (port map,
  compose profiles, troubleshooting).

[rfc-api-repo]: https://github.com/donaldgifford/rfc-api
[rfc-api-openapi]: https://github.com/donaldgifford/rfc-api/blob/main/api/openapi.yaml
[rfc-api-design-0001]: https://github.com/donaldgifford/rfc-api/blob/main/docs/design/0001-rfc-api-http-server-go-net-http-structure.md
[rfc-api-design-0002]: https://github.com/donaldgifford/rfc-api/blob/main/docs/design/0002-documenttype-extensibility-for-multiple-content-types.md
[rfc-api-dev]: https://github.com/donaldgifford/rfc-api/tree/main/docs/development
