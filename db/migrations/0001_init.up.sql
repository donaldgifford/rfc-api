-- IMPL-0002 Phase 1: initial schema.
--
-- Documents, authors, links, discussions, discussion_participants, and
-- the worker's jobs table. See docs/impl/0002-*.md for the per-column
-- rationale; cross-cutting decisions live in the Resolved Decisions
-- section.
--
-- Load-bearing invariants:
--   * documents.type is a free string filled from the registry;
--     DocumentType per DESIGN-0002 is a parameter, not a column prefix.
--   * documents.id is the canonical display id ("RFC-0001"), a single
--     string column — the URL numeric form is derived at read time.
--   * extensions is jsonb with a GIN index (RD4); labels is text[]
--     with a GIN index (RD5).
--   * jobs.dedup_key is opaque; each job kind formats its own value.
--     UNIQUE (kind, dedup_key) is the idempotency seam that IMPL-0003's
--     queue relies on.

-- documents: the primary table. One row per canonical document.
CREATE TABLE documents (
    id             text        PRIMARY KEY,
    type           text        NOT NULL,
    title          text        NOT NULL,
    status         text,
    body           text,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    labels         text[]      NOT NULL DEFAULT '{}',
    extensions     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    source_repo    text,
    source_path    text,
    source_commit  text
);

-- Keyset pagination indexes. The list endpoints (/api/v1/docs and
-- /api/v1/{type}) sort by (created_at DESC, id ASC) per DESIGN-0001
-- #API surface. A single composite index serves each.
CREATE INDEX documents_created_id_idx
    ON documents (created_at DESC, id ASC);

CREATE INDEX documents_type_created_id_idx
    ON documents (type, created_at DESC, id ASC);

-- GIN indexes for filter-by-label and filter-by-extension queries.
CREATE INDEX documents_labels_gin_idx
    ON documents USING GIN (labels);

CREATE INDEX documents_extensions_gin_idx
    ON documents USING GIN (extensions);

-- authors: one row per (document_id, seq). seq preserves the author
-- order that appears in the source document so downstream rendering
-- is stable across reads.
CREATE TABLE authors (
    document_id  text    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    seq          int     NOT NULL,
    name         text    NOT NULL,
    email        text,
    handle       text,
    PRIMARY KEY (document_id, seq)
);

CREATE INDEX authors_document_idx ON authors (document_id);

-- links: one row per cross-reference edge. target_id is not a FK
-- because incoming links can point at documents that do not exist yet
-- (ingest order is not guaranteed). The handler resolves dangling
-- links at read time.
CREATE TABLE links (
    source_id  text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    target_id  text NOT NULL,
    direction  text NOT NULL CHECK (direction IN ('incoming', 'outgoing')),
    label      text,
    PRIMARY KEY (source_id, target_id, direction)
);

CREATE INDEX links_source_idx ON links (source_id);
CREATE INDEX links_target_idx ON links (target_id);

-- discussions: per-document PR review thread summary. One row per
-- document at most. Participants live in the join table below so
-- /discussion can return participant lists without array wrangling.
CREATE TABLE discussions (
    document_id    text        PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    url            text,
    comment_count  int         NOT NULL DEFAULT 0,
    last_activity  timestamptz,
    last_synced_at timestamptz
);

CREATE TABLE discussion_participants (
    document_id  text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    seq          int  NOT NULL,
    handle       text NOT NULL,
    name         text,
    email        text,
    PRIMARY KEY (document_id, seq)
);

CREATE INDEX discussion_participants_document_idx
    ON discussion_participants (document_id);

-- jobs: the worker queue (IMPL-0002 RD8 / IMPL-0003 RD9). Shape
-- lives here; leasing, retry, and dead-letter semantics are in
-- IMPL-0003.
--
-- gen_random_uuid() is built-in in PostgreSQL 13+; no pgcrypto
-- extension needed.
CREATE TABLE jobs (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        text        NOT NULL,
    dedup_key   text        NOT NULL,
    payload     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    state       text        NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'leased', 'dead')),
    attempts    int         NOT NULL DEFAULT 0,
    locked_by   text,
    locked_at   timestamptz,
    run_after   timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (kind, dedup_key)
);

-- The lease query is:
--   SELECT ... FROM jobs
--    WHERE state = 'queued' AND run_after <= now() AND kind = ANY($1)
--    ORDER BY run_after, created_at
--    FOR UPDATE SKIP LOCKED LIMIT $2
-- A compound index on (state, kind, run_after) lets Postgres skip
-- the filter scan and walk the index straight to the lease candidates.
CREATE INDEX jobs_lease_idx ON jobs (state, kind, run_after);
