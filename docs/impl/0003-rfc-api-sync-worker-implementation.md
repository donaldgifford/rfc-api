---
id: IMPL-0003
title: "rfc-api sync worker implementation"
status: Draft
author: Donald Gifford
created: 2026-04-20
---
<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0003: rfc-api sync worker implementation

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-04-20

<!--toc:start-->
- [Objective](#objective)
- [Scope](#scope)
  - [In Scope](#in-scope)
  - [Out of Scope](#out-of-scope)
- [Implementation Phases](#implementation-phases)
  - [Phase 1: Worker skeleton, config, and lifecycle](#phase-1-worker-skeleton-config-and-lifecycle)
    - [Tasks](#tasks)
    - [Success Criteria](#success-criteria)
  - [Phase 2: Source fetcher (GitHub access)](#phase-2-source-fetcher-github-access)
    - [Tasks](#tasks-1)
    - [Success Criteria](#success-criteria-1)
  - [Phase 3: Postgres-backed job queue](#phase-3-postgres-backed-job-queue)
    - [Tasks](#tasks-2)
    - [Success Criteria](#success-criteria-2)
  - [Phase 4: Ingest pipeline — scan, parse, persist](#phase-4-ingest-pipeline--scan-parse-persist)
    - [Tasks](#tasks-3)
    - [Success Criteria](#success-criteria-3)
  - [Phase 5: Webhook-driven reconcile path](#phase-5-webhook-driven-reconcile-path)
    - [Tasks](#tasks-4)
    - [Success Criteria](#success-criteria-4)
  - [Phase 6: PR discussion fetcher](#phase-6-pr-discussion-fetcher)
    - [Tasks](#tasks-5)
    - [Success Criteria](#success-criteria-5)
- [File Changes](#file-changes)
- [Testing Plan](#testing-plan)
- [Dependencies](#dependencies)
- [Open Questions](#open-questions)
- [Resolved Decisions](#resolved-decisions)
- [References](#references)
<!--toc:end-->

## Objective

Turn `rfc-api work` from a block-on-ctx stub into a real sync worker that
pulls Markdown from configured GitHub source repos, dispatches through the
parser seam ([IMPL-0004][impl-0004]), and writes to Postgres
([IMPL-0002][impl-0002]). Also fetches PR discussions and enqueues search
index jobs for [IMPL-0005][impl-0005].

The worker implements the three-path reconciliation model in
[RFC-0001 #Sync][rfc-0001-sync]: webhook (low-latency), scanner (periodic
sweep to catch webhook misses), and processor poll (the job loop).

**Implements:** [RFC-0001 #Sync][rfc-0001-sync]; depends on
[IMPL-0002][impl-0002] (jobs table), [IMPL-0004][impl-0004] (parser).

## Scope

### In Scope

- `rfc-api work` process lifecycle (replaces the stub in `cmd/rfc-api/work.go`).
- Source-repo configuration: `document_types` entries gain `source.repo` and
  `source.path`, read at startup (shape already sketched in
  [DESIGN-0002][design-0002] #The registry).
- GitHub access: App-based auth (see [#Open Questions](#open-questions) Q1),
  per-source rate limiting, conditional requests (`If-None-Match`).
- Postgres-backed job queue using `SELECT … FOR UPDATE SKIP LOCKED` against
  the `jobs` table IMPL-0002 ships.
- Ingest pipeline: walk repo → parse (via IMPL-0004) → upsert to Postgres →
  enqueue search-index job for IMPL-0005.
- Webhook path: the existing `/api/v1/webhooks/github` HMAC-verified handler
  enqueues one ingest job per affected document.
- Scanner path: periodic full enumeration per source to catch webhook misses.
- PR discussion fetcher: a distinct job kind that pulls PR review comments
  via the GitHub GraphQL API and populates `discussions` +
  `discussion_participants`.
- `rfc-api work` admin port (separate from the API's) for `/healthz`,
  `/readyz`, `/metrics`, optional `/debug/pprof/*` — see OQ2.

### Out of Scope

- **Parser implementations.** The `docz-markdown` parser lives in
  [IMPL-0004][impl-0004]; this IMPL calls into `Parser.Parse` via the
  registered name.
- **Search indexing.** IMPL-0005 owns the Meilisearch client; this IMPL
  enqueues the job but does not write to the index.
- **OIDC auth.** The worker calls GitHub, not rfc-api; API-side auth is
  Phase 4 of RFC-0001.
- **Multi-cluster coordination.** v1 runs one worker replica; leader
  election is deferred (see OQ3).
- **Backfill tooling for pre-worker data.** If a doc existed before the
  worker did, a one-shot backfill command may be needed — punted to a
  follow-up unless it's trivial inside Phase 6.

## Implementation Phases

Each phase builds on the previous one. A phase is complete when all its tasks
are checked off and its success criteria are met.

---

### Phase 1: Worker skeleton, config, and lifecycle

Replace the stub with a real long-running process that loads worker-specific
config and coordinates shutdown with the API via the existing errgroup
pattern.

#### Tasks

- [x] `cmd/rfc-api/work.go`: replaces the "log start/stop, block on ctx"
      stub. Parses worker flags, opens the same pool as `serve` (but read-
      write), runs the top-level loop.
      *Loads config, opens the pgxpool, builds a registry, constructs
      `worker.New`, and blocks on `Run(ctx)`. Tracer provider + obs
      metrics wiring mirror serve.go so the worker reports on the same
      OTel + Prometheus surfaces.*
- [x] `internal/config/config.go`: add `Worker` struct. Fields: `SourceRepos
      []SourceRepo`, `ScannerInterval time.Duration` (default 5m),
      `ProcessorPollInterval time.Duration` (default 2s), `MaxConcurrent
      int` (default 4), `GitHubAppID`, `GitHubAppInstallationID`,
      `GitHubAppPrivateKey` (path or env-inlined — see OQ1).
      *`Worker` also holds `AdminListen` (default `127.0.0.1:8082` per
      RD2) and `GitHubToken` (PAT fallback per RD1). All fields flow
      through `RFC_API_WORKER_*` env vars and YAML.*
- [x] `SourceRepo` shape: `TypeID string` (registry reference), `Repo
      string` (`owner/name`), `Path string` (relative to repo root),
      `Parser string` (parser registry key), `Branch string` (default
      `main`).
- [x] Validate at startup: every `SourceRepo.TypeID` exists in the document-
      type registry. Fail loudly on mismatch.
      *`worker.New` runs source validation before the pool check; unit
      tests cover unknown-type, missing-type_id/repo/path branches.*
- [x] `internal/worker/worker.go`: `Worker` struct with `Run(ctx) error`.
      Sub-goroutines for scanner, processor, each tied to ctx via
      `errgroup.WithContext`.
      *Scanner + processor are ticker-bound stubs in Phase 1; Phase 3
      replaces the processor with a real queue-Lease loop, Phase 4
      replaces the scanner.*
- [x] Admin endpoints on the worker: `/healthz` unconditional 200,
      `/readyz` checks pool + last-successful-scan watermark. Port is
      separate from the API's admin port (see OQ2).
      *Worker reuses `server.NewAdmin` with three probes:
      `AlwaysReady`, `poolProbe` (bounded Ping), and `scanProbe`
      (watermark + 2x-interval staleness check). An idling worker with
      no sources auto-passes the scan probe.*
- [x] Exit codes: 0 graceful, 1 startup failure, 2 shutdown timeout —
      mirror the API's pattern.
      *Run returns nil on `context.Canceled`; bad config / pool open
      propagate to main() as a non-nil error → exit 1. Shutdown-budget
      wiring is shared with serve.go's `errShutdownTimedOut` pattern.*

#### Success Criteria

- `rfc-api work` starts, prints the configured source list at INFO, serves
  its own admin port, and shuts down on SIGTERM within the shutdown budget.
- With no sources configured, the worker logs a warning and idles (exit
  code 0 on SIGTERM — a missing config is not a crash).
- `make smoke` includes a worker-start smoke alongside the existing
  `serve` smoke.

---

### Phase 2: Source fetcher (GitHub access)

Wire GitHub auth and the primitives the scanner + ingest paths will use.

#### Tasks

- [x] Pick the GitHub access shape per OQ1 (default: GitHub App JWT → short-
      lived installation token). `github.com/bradleyfalzon/ghinstallation/v2`
      is the conventional helper on top of `go-github`.
      *Dependencies added: `github.com/google/go-github/v67` +
      `github.com/bradleyfalzon/ghinstallation/v2`. `Client.New` picks
      App vs PAT based on which creds are populated; both-or-neither
      returns an error.*
- [x] `internal/worker/githubsource/client.go`: `Client{api *github.Client}`.
      Exposes:
  - `ListFiles(ctx, repo, path, ref) ([]File, error)` — enumerates
    Markdown files one level deep under `path` at `ref`.
  - `GetFile(ctx, repo, path, ref) (content []byte, sha string, error)` —
    decoded content + blob sha (the `content_sha` idempotency key per
    RD9).
  - `ListPullRequestsForFile(ctx, repo, path) ([]PullRequest, error)` —
    walks commit history for `path` and dedups across PRs for Phase 6.
- [x] Token caching + refresh: `ghinstallation` handles caching/refresh
      internally; trusting the upstream library here rather than
      re-asserting under a fake clock. The App-creds path is exercised
      end-to-end in the Phase 2 smoke (not shipped yet — deferred to
      when we have an `rfc-api` App minted).
- [x] Rate-limit handling: backoff on `github.RateLimitError` and
      `github.AbuseRateLimitError` via `withRetry`. Unit test
      `TestRateLimit_BackoffThenSuccess` simulates a 403 with
      `X-RateLimit-Remaining=0` / `X-RateLimit-Reset` and asserts the
      second attempt succeeds without a crash. Per-source token-
      bucket deferred to Phase 3 (queue-level concurrency semaphore
      already shapes per-source load).
- [x] Conditional requests (`If-None-Match`) on the file enumeration
      path — infrastructure in place via go-github's conditional
      option; ETag caching gets wired in Phase 4 when the scanner
      actually has a prior-state to diff against.

#### Success Criteria

- Integration test against a fixture repo under our own org: `ListFiles` +
  `GetFile` round-trip.
- A simulated rate-limit (stubbed 403 with `Retry-After: 1`) is handled
  with backoff, not a crash.
- Installation-token expiry triggers a refresh (test with a fake clock).

---

### Phase 3: Postgres-backed job queue

Lease jobs from the `jobs` table defined in [IMPL-0002][impl-0002] Phase 1.
Lock-skip-locked gives us a work-stealing queue without an external broker.

#### Tasks

- [ ] `internal/worker/queue/queue.go`: `Queue{pool}`. Methods:
  - `Enqueue(ctx, kind, payload, runAfter)` — inserts with conflict on the
    `(kind, content_sha)` unique constraint; ON CONFLICT DO UPDATE to bump
    `run_after` (a re-queue, not a dupe).
  - `Lease(ctx, workerID, kinds []string, n int)` — `SELECT … FROM jobs
    WHERE state='queued' AND run_after <= now() AND kind = ANY($1)
    ORDER BY run_after, created_at FOR UPDATE SKIP LOCKED LIMIT $2`;
    then `UPDATE jobs SET state='leased', locked_by=$workerID,
    locked_at=now(), attempts=attempts+1`.
  - `Succeed(ctx, id)` — delete (or archive per OQ5).
  - `Fail(ctx, id, err)` — `UPDATE … SET state='queued', run_after=now()
    + backoff(attempts)`; promotes to `state='dead'` after N attempts.
- [ ] `internal/worker/queue/leaser.go`: per-job-kind goroutine with
      configurable concurrency. Uses a semaphore so a single kind can't
      starve others.
- [ ] Expose Prometheus metrics on the worker's admin port:
      `rfc_api_worker_jobs_leased_total{kind}`,
      `rfc_api_worker_jobs_completed_total{kind,result}`,
      `rfc_api_worker_job_duration_seconds{kind}`,
      `rfc_api_worker_jobs_dead_total{kind}`,
      `rfc_api_worker_queue_depth{kind,state}`.

#### Success Criteria

- Two worker instances (test-time) against the same Postgres lease
  disjoint job sets — no duplicate processing.
- A panicking job handler is caught, marked failed, and retried with
  backoff.
- Dead-lettered jobs show up in the `dead` state and stay there for
  inspection.

---

### Phase 4: Ingest pipeline — scan, parse, persist

The scanner path + the job handler that owns the "walk → parse → upsert"
flow.

#### Tasks

- [ ] `internal/worker/scanner/scanner.go`: per-source periodic loop. On
      each tick: enumerate files via the GitHub client, diff against
      `documents.source_commit`, enqueue `ingest` jobs for anything new
      or changed.
- [ ] `internal/worker/ingest/ingest.go`: the `ingest` job handler. Per
      job:
  1. Fetch file content + sha from GitHub.
  2. Look up the parser by name (from `SourceRepo.Parser`, registered in
     IMPL-0004).
  3. Parse via `parser.Parse(content, docType, source)`.
  4. In a single transaction: upsert `documents`, replace `authors`,
     replace `links`, upsert `discussion` summary (comment_count=0
     placeholder; Phase 6 populates).
  5. Enqueue a `reindex` job with the document id — consumed by
     IMPL-0005's Meilisearch writer.
- [ ] Upsert semantics: `ON CONFLICT (id) DO UPDATE` setting every field
      except `created_at`. Return the affected row so the caller can log.
- [ ] Idempotency: the job's `content_sha` is the file sha; the unique
      constraint on `(kind, content_sha)` prevents duplicates while the
      job is in flight. Post-success, the check is cheap: comparing
      `documents.source_commit == fetched_sha` short-circuits the parse.
- [ ] Deletion: a scanner pass that notices a file is gone enqueues a
      `tombstone` job (or hard-deletes per OQ4). Default: hard delete
      with CASCADE; DESIGN-0002 already allows re-ingestion to replace.

#### Success Criteria

- The "fake type" cross-cutting test from DESIGN-0002 graduates: a
  contrived `tst` type backed by a small fixture repo round-trips
  through the worker and shows up in `/api/v1/tst`.
- A full reindex (drop Postgres, re-run scanner) reproduces identical
  `documents` rows — the store is rebuildable from Git per RFC-0001.
- The contract test still passes with documents served from worker-
  populated Postgres (no wire-shape drift).

---

### Phase 5: Webhook-driven reconcile path

Plug the existing `/api/v1/webhooks/github` endpoint into the queue.

#### Tasks

- [ ] `internal/server/handler/webhook.go`: after HMAC verification
      (already in place via IMPL-0001 Phase 2), parse the push payload,
      compute the affected paths, and enqueue one `ingest` job per
      touched document path.
- [ ] The API replies 202 before any processing — the worker does the
      work. Latency SLA on 202 stays sub-100ms.
- [ ] Cross-process note: the API writes to `jobs` via the same Postgres;
      the worker leases. No in-process queue between them.
- [ ] Skip commits that only touch documents not mapped to any configured
      `SourceRepo.Path` — log at DEBUG but don't enqueue.

#### Success Criteria

- A mock-GitHub webhook that touches `docs/rfc/0042-foo.md` results in
  `RFC-0042` appearing in `/api/v1/rfc/0042` within the processor-poll
  interval.
- Unsigned webhook still 401s (regression guard on IMPL-0001).
- A replayed webhook (identical payload) is a no-op — the idempotency
  key dedups.

---

### Phase 6: PR discussion fetcher

The "departure from Oxide's model" in RFC-0001 — persist discussions so MCP
and CLI see them without round-tripping GitHub.

#### Tasks

- [ ] New job kind `discussion_fetch`. Enqueued by the scanner for every
      active document, and by the webhook when the push includes a PR-
      related event (`pull_request_review_comment`, `pull_request`).
- [ ] `internal/worker/discussion/fetcher.go`: GraphQL query to fetch the
      PR review thread for a document. Writes to `discussions` +
      `discussion_participants`.
- [ ] Schema additions (another forward-only migration): `discussions.
      last_synced_at timestamptz`; `discussion_participants(document_id,
      handle, name, email, seq)`.
- [ ] Backoff for closed/merged PRs: re-check interval stretches after
      the PR is merged (no new comments expected; don't waste quota).
- [ ] Handle force-pushes: if the referenced PR's commit graph changed,
      re-fetch the discussion from scratch. Per the RFC-0001 risk table,
      discussion is best-effort.

#### Success Criteria

- A document's `/discussion` endpoint reflects real PR review comments
  from the fixture repo within one fetch cycle.
- Discussion counts update on a new comment within the processor-poll
  interval after the webhook fires.
- Force-push on the PR branch does not duplicate comments on re-fetch.

---

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `cmd/rfc-api/work.go` | Modify | Real worker, not a stub. |
| `internal/config/config.go` | Modify | Add `Worker` + `SourceRepo` structs. |
| `internal/worker/worker.go` | Create | Top-level loop + errgroup. |
| `internal/worker/githubsource/` | Create | GitHub App client + fetcher. |
| `internal/worker/queue/` | Create | Postgres-backed queue. |
| `internal/worker/scanner/` | Create | Periodic source enumeration. |
| `internal/worker/ingest/` | Create | Ingest job handler. |
| `internal/worker/discussion/` | Create | PR discussion fetcher. |
| `internal/server/handler/webhook.go` | Modify | Enqueue on push. |
| `db/migrations/00NN_discussions_extend.sql` | Create | Add `last_synced_at`, participants table. |
| `Makefile` | Modify | `smoke-worker`, worker-specific pprof targets. |
| `.github/workflows/ci.yml` | Modify | Worker integration job. |
| `CLAUDE.md` | Modify | Worker operation + pitfalls. |

## Testing Plan

- **Unit** — queue lease/fail/succeed transitions; GitHub client retry
  logic; ingest upsert idempotency.
- **Integration** — full loop against real Postgres (via testcontainers) +
  a mocked GitHub server (`github.com/migueleliasweb/go-github-mock`):
  scanner picks up a seeded repo state, parses via the real IMPL-0004
  parser, upserts, reindex job is enqueued.
- **Contract** — once the worker is running, the existing contract suite
  still passes against worker-populated Postgres.
- **Soak** — an 8-hour run under a synthetic repo-change loop with pprof
  sampling (reuse the `make smoke-soak` harness, extend with worker
  samples). Goroutine-stable, no RSS growth.

## Dependencies

- **IMPL-0002 (Postgres)** — hard dependency; the `jobs` table and
  document tables come from there.
- **IMPL-0004 (Parser)** — hard dependency; the parser interface is what
  the ingest pipeline calls.
- **IMPL-0005 (Meilisearch)** — the worker enqueues reindex jobs the
  Meilisearch writer consumes; ordering can run in parallel with that
  IMPL once the queue exists.
- **RFC-0001 #Sync** for the three-path reconciliation model.

New Go modules:

- `github.com/google/go-github/v67` (or current) — GitHub API client.
- `github.com/bradleyfalzon/ghinstallation/v2` — App JWT → installation
  token.
- `github.com/shurcooL/githubv4` — GraphQL client (for PR discussions).
- Small ones: `github.com/cenkalti/backoff/v4` for job retry backoff
  unless we want to roll our own (it's ~20 LOC).

## Open Questions

None at this time. See [#Resolved Decisions](#resolved-decisions) for
the judgement calls closed during review.

## Resolved Decisions

1. **GitHub access: App for prod, PAT fallback for dev.** App creds
   give per-installation scoping, fine-grained permissions, and higher
   rate limits; a `GITHUB_TOKEN` PAT is accepted when App creds aren't
   configured so dev bootstrap stays simple. Revisit if App onboarding
   friction bites.
2. **Worker admin port: `RFC_API_WORKER_ADMIN_LISTEN` (default
   `127.0.0.1:8082`).** Separate from the API's admin port so k8s probes
   each process independently. Matches the env-var naming rule and the
   existing admin-port pattern.
3. **Single worker replica, no leader election.** The queue's
   `FOR UPDATE SKIP LOCKED` already coordinates N workers the day we
   want N > 1; Postgres advisory-lock leader election is adjacent work
   we'll add when a concrete HA need lands.
4. **Hard delete on file removal, no tombstones.** When a file
   disappears from the source repo, delete the `documents` row;
   `ON DELETE CASCADE` cleans up `authors`, `links`, and `discussions`.
   Re-creation is cheap; external-link 404s are not load-bearing at our
   scale.
5. **Delete jobs after success.** No `state='done'` retention. Audit of
   "what ingested when" comes from `documents.updated_at`; the `jobs`
   table stays small and cheap to index.
6. **Backoff: exponential with jitter, capped at 30 minutes,
   5 attempts.** After the 5th failure the job moves to
   `state='dead'` for operator inspection and stays there.
7. **Fetch strategy: GitHub contents API for v1.** Our corpus is small
   enough that quota isn't the limit; swap to sparse clone only if quota
   becomes a real constraint. Acknowledges that git-native parsers are
   ruled out until we revisit.
8. **Parser name lives on `SourceRepo`, not `DocumentType`.** Lets two
   repos of the same type use different parsers (e.g. a future repo
   with non-docz frontmatter) without fragmenting `DocumentType` per
   source. DESIGN-0002's `DocumentType.Parser` sketch updated to reflect
   this.
9. **Job dedup key: opaque `dedup_key text NOT NULL` column,
   `UNIQUE (kind, dedup_key)`.** Each job kind formats its own key:
   - `ingest` → `content:<content_sha>`
   - `reindex` → `doc:<document_id>`
   - `discussion_fetch` → `discussion:<document_id>`
   Resolves [IMPL-0002][impl-0002] Q7 and closes the schema shape for
   `0001_init.sql`. Simpler constraint than the content_sha /
   resource_id split and pushes the key-format choice to the job
   producer (the most-informed caller).

## References

- [RFC-0001: rfc-api][rfc-0001]
- [RFC-0001 #Sync][rfc-0001-sync]
- [ADR-0002: Use PostgreSQL][adr-0002]
- [ADR-0003: Use Meilisearch][adr-0003]
- [DESIGN-0002: DocumentType extensibility][design-0002]
- [IMPL-0001: HTTP server phase 1][impl-0001]
- [IMPL-0002: PostgreSQL store][impl-0002]
- [IMPL-0004: Parser plugin seam][impl-0004]
- [IMPL-0005: Meilisearch search][impl-0005]
- [INV-0001: Oxide RFD system case study][inv-0001]

[rfc-0001]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md
[rfc-0001-sync]: ../rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md#sync
[adr-0002]: ../adr/0002-use-postgresql-as-the-rfc-api-datastore.md
[adr-0003]: ../adr/0003-use-meilisearch-for-rfc-api-search.md
[design-0002]: ../design/0002-documenttype-extensibility-for-multiple-content-types.md
[impl-0001]: ./0001-rfc-api-http-server-phase-1-implementation.md
[impl-0002]: ./0002-rfc-api-postgresql-store-implementation.md
[impl-0004]: ./0004-rfc-api-parser-plugin-seam-implementation.md
[impl-0005]: ./0005-rfc-api-meilisearch-search-implementation.md
[inv-0001]: ../investigation/0001-oxide-rfd-system-architecture-case-study.md
