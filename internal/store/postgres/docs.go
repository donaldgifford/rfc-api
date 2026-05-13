package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store"
	"github.com/donaldgifford/rfc-api/internal/store/list"
)

// Docs is the pgx-backed implementation of store.Docs. Read methods
// take a parent ctx and each issue one or more queries via the pool;
// no implicit transaction is opened because all current methods are
// read-only single-statement workloads.
type Docs struct {
	pool *pgxpool.Pool
}

// NewDocs wires a Docs store over an existing pool. The caller owns
// the pool lifecycle (see NewPool).
func NewDocs(pool *pgxpool.Pool) *Docs {
	return &Docs{pool: pool}
}

// Column list reused across Get + List so any schema change only has
// one site to update.
const documentColumns = `
    id, type, title, status, body,
    created_at, updated_at,
    labels, extensions,
    source_repo, source_path, source_commit`

// Get implements store.Docs.Get. Fetches the base row plus the
// document's authors and discussion in parallel issue-but-sequential
// form; returns domain.ErrNotFound (wrapped) when the row is missing.
func (d *Docs) Get(ctx context.Context, id domain.DocumentID) (domain.Document, error) {
	doc, err := d.getDocument(ctx, id)
	if err != nil {
		return domain.Document{}, err
	}

	authors, err := d.Authors(ctx, id)
	if err != nil {
		return domain.Document{}, err
	}
	doc.Authors = authors

	discussion, err := d.discussionOrNil(ctx, id)
	if err != nil {
		return domain.Document{}, err
	}
	doc.Discussion = discussion

	return doc, nil
}

// List implements store.Docs.List with keyset pagination. The active
// sort is selected by list.Sort; the type filter is OR-within-field
// (DESIGN-0003 #Filter-semantics). Empty TypeIDs is the cross-type
// /api/v1/docs surface.
//
// List returns only the document-level columns. Callers that need
// authors / links / discussion hit the dedicated sub-resource
// endpoints (which run through those store methods).
func (d *Docs) List(ctx context.Context, opts ...list.Option) (store.Page, error) {
	cfg := list.Apply(opts...)
	if cfg.Limit <= 0 {
		return store.Page{}, fmt.Errorf("%w: limit must be positive", domain.ErrInvalidInput)
	}

	total, err := d.countDocuments(ctx, cfg.TypeIDs)
	if err != nil {
		return store.Page{}, err
	}

	items, err := d.listDocuments(ctx, cfg)
	if err != nil {
		return store.Page{}, err
	}

	page := store.Page{Total: total}
	if len(items) > cfg.Limit {
		last := items[cfg.Limit-1]
		page.NextCursor = nextCursor(&last, cfg.Sort)
		page.Items = items[:cfg.Limit]
	} else {
		page.Items = items
	}
	return page, nil
}

// CountAll returns the unfiltered document count. Used by the
// handler to populate X-Total-Count-Unfiltered when a filter is
// active (DESIGN-0003 #Total-count-headers; IMPL-0007 #OQ5).
func (d *Docs) CountAll(ctx context.Context) (int, error) {
	var total int
	if err := d.pool.QueryRow(ctx,
		`SELECT count(*) FROM documents`).Scan(&total); err != nil {
		return 0, upstream("count all documents", err)
	}
	return total, nil
}

// nextCursor builds the NextCursor a paginated query returns to the
// caller. Time-based sorts populate SortValue; id sorts leave it
// zero (the cursor encoder emits an empty K[0] slot for those).
func nextCursor(last *domain.Document, s list.Sort) *list.Cursor {
	cur := &list.Cursor{Sort: s, ID: last.ID}
	switch s {
	case list.SortCreatedDesc, list.SortCreatedAsc:
		cur.SortValue = last.CreatedAt
	case list.SortUpdatedDesc, list.SortUpdatedAsc:
		cur.SortValue = last.UpdatedAt
	}
	return cur
}

// CountByType returns the number of documents per type id. Used by
// the reindex drift check to compare Postgres against Meili's parent-
// id distribution.
func (d *Docs) CountByType(ctx context.Context) (map[string]int, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT type, count(*) FROM documents GROUP BY type`)
	if err != nil {
		return nil, upstream("query count by type", err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var (
			t string
			n int
		)
		if err := rows.Scan(&t, &n); err != nil {
			return nil, upstream("scan count row", err)
		}
		out[t] = n
	}
	if err := rows.Err(); err != nil {
		return nil, upstream("iterate count rows", err)
	}
	return out, nil
}

// AllIDs returns every document id in the store, ordered for a stable
// reindex pass. Callers (rfc-api reindex) fan these out into `reindex`
// jobs so the worker rebuilds the search index from authoritative
// Postgres state. The v1 corpus fits comfortably in memory — no
// pagination here.
func (d *Docs) AllIDs(ctx context.Context) ([]domain.DocumentID, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT id FROM documents ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, upstream("query all ids", err)
	}
	defer rows.Close()

	var out []domain.DocumentID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, upstream("scan id row", err)
		}
		out = append(out, domain.DocumentID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, upstream("iterate id rows", err)
	}
	return out, nil
}

// Links returns both outgoing edges (this doc → others) and incoming
// edges (others → this doc). Incoming edges are discovered by scanning
// the links table for target_id = id.
func (d *Docs) Links(ctx context.Context, id domain.DocumentID) ([]domain.Link, error) {
	if err := d.ensureExists(ctx, id); err != nil {
		return nil, err
	}

	rows, err := d.pool.Query(ctx, `
		SELECT target_id, direction, label
		  FROM links
		 WHERE source_id = $1
		 UNION ALL
		SELECT source_id AS target_id, 'incoming' AS direction, label
		  FROM links
		 WHERE target_id = $1 AND direction = 'outgoing'
		 ORDER BY direction, 1`, string(id))
	if err != nil {
		return nil, upstream("query links", err)
	}
	defer rows.Close()

	var out []domain.Link
	for rows.Next() {
		var (
			target    string
			direction string
			label     *string
		)
		if err := rows.Scan(&target, &direction, &label); err != nil {
			return nil, upstream("scan link row", err)
		}
		link := domain.Link{
			Direction: domain.LinkDirection(direction),
			Target:    domain.DocumentID(target),
		}
		if label != nil {
			link.Label = *label
		}
		out = append(out, link)
	}
	if err := rows.Err(); err != nil {
		return nil, upstream("iterate link rows", err)
	}
	return out, nil
}

// Discussion returns the PR-discussion summary for a document. A
// document without a discussion row returns a zero-value Discussion
// (not an error); the memory reference behaves the same way.
func (d *Docs) Discussion(ctx context.Context, id domain.DocumentID) (domain.Discussion, error) {
	if err := d.ensureExists(ctx, id); err != nil {
		return domain.Discussion{}, err
	}

	disc, err := d.discussionOrNil(ctx, id)
	if err != nil {
		return domain.Discussion{}, err
	}
	if disc == nil {
		return domain.Discussion{}, nil
	}

	participants, err := d.discussionParticipants(ctx, id)
	if err != nil {
		return domain.Discussion{}, err
	}
	disc.Participants = participants
	return *disc, nil
}

// Authors returns the author list in the order stored (by seq).
func (d *Docs) Authors(ctx context.Context, id domain.DocumentID) ([]domain.Author, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT name, email, handle
		  FROM authors
		 WHERE document_id = $1
		 ORDER BY seq`, string(id))
	if err != nil {
		return nil, upstream("query authors", err)
	}
	defer rows.Close()

	var out []domain.Author
	for rows.Next() {
		var a domain.Author
		var email, handle *string
		if err := rows.Scan(&a.Name, &email, &handle); err != nil {
			return nil, upstream("scan author row", err)
		}
		if email != nil {
			a.Email = *email
		}
		if handle != nil {
			a.Handle = *handle
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, upstream("iterate author rows", err)
	}
	return out, nil
}

// Revisions is a Phase-3 stub. The revisions table and the worker
// that populates it arrive with IMPL-0003; until then the store
// returns an empty slice. The endpoint stays wired so callers can
// observe the shape.
func (d *Docs) Revisions(ctx context.Context, id domain.DocumentID) ([]store.Revision, error) {
	if err := d.ensureExists(ctx, id); err != nil {
		return nil, err
	}
	return []store.Revision{}, nil
}

// Upsert is a Phase-3 stub per IMPL-0002 RD7. Present on the interface
// so IMPL-0003 and IMPL-0005 can target a stable contract; returns a
// well-known error until the worker is wired.
// Upsert inserts or replaces a document + its authors + its links in
// a single transaction. Preserves documents.created_at on update so
// the registered-at timestamp is stable across re-ingests (RFC-0001
// Sync: the store is rebuildable from Git but CreatedAt is an
// archival signal). Discussions are owned by the Phase-6 discussion
// fetcher, not this path.
func (d *Docs) Upsert(ctx context.Context, doc *domain.Document) error {
	if doc == nil {
		return errors.New("upsert: nil document")
	}
	if doc.ID == "" {
		return fmt.Errorf("%w: document id is required", domain.ErrInvalidInput)
	}
	if doc.Type == "" {
		return fmt.Errorf("%w: document type is required", domain.ErrInvalidInput)
	}

	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return upstream("begin upsert", err)
	}
	defer func() {
		//nolint:errcheck,gosec // rollback on commit-success is a no-op; on failure path the caller already has the cause.
		tx.Rollback(ctx)
	}()

	if err := upsertDocument(ctx, tx, doc); err != nil {
		return err
	}
	if err := replaceAuthors(ctx, tx, doc); err != nil {
		return err
	}
	if err := replaceLinks(ctx, tx, doc); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return upstream("commit upsert", err)
	}
	return nil
}

func upsertDocument(ctx context.Context, tx pgx.Tx, doc *domain.Document) error {
	const stmt = `
		INSERT INTO documents (
			id, type, title, status, body,
			created_at, updated_at,
			labels, extensions,
			source_repo, source_path, source_commit
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
			type          = EXCLUDED.type,
			title         = EXCLUDED.title,
			status        = EXCLUDED.status,
			body          = EXCLUDED.body,
			updated_at    = EXCLUDED.updated_at,
			labels        = EXCLUDED.labels,
			extensions    = EXCLUDED.extensions,
			source_repo   = EXCLUDED.source_repo,
			source_path   = EXCLUDED.source_path,
			source_commit = EXCLUDED.source_commit
	`
	labels := doc.Labels
	if labels == nil {
		labels = []string{}
	}
	ext := doc.Extensions
	if ext == nil {
		ext = map[string]any{}
	}
	if _, err := tx.Exec(ctx, stmt,
		string(doc.ID), doc.Type, doc.Title, doc.Status, doc.Body,
		nonZeroTime(doc.CreatedAt), nonZeroTime(doc.UpdatedAt),
		labels, ext,
		doc.Source.Repo, doc.Source.Path, doc.Source.Commit,
	); err != nil {
		return upstream("upsert documents", err)
	}
	return nil
}

func replaceAuthors(ctx context.Context, tx pgx.Tx, doc *domain.Document) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM authors WHERE document_id = $1`, string(doc.ID)); err != nil {
		return upstream("clear authors", err)
	}
	if len(doc.Authors) == 0 {
		return nil
	}
	const stmt = `INSERT INTO authors (document_id, seq, name, email, handle)
	              VALUES ($1,$2,$3,$4,$5)`
	for i, a := range doc.Authors {
		if _, err := tx.Exec(ctx, stmt, string(doc.ID), i, a.Name, a.Email, a.Handle); err != nil {
			return upstream(fmt.Sprintf("insert author[%d]", i), err)
		}
	}
	return nil
}

func replaceLinks(ctx context.Context, tx pgx.Tx, doc *domain.Document) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM links WHERE source_id = $1`, string(doc.ID)); err != nil {
		return upstream("clear links", err)
	}
	if len(doc.Links) == 0 {
		return nil
	}
	const stmt = `INSERT INTO links (source_id, target_id, direction, label)
	              VALUES ($1,$2,$3,$4)`
	for i, l := range doc.Links {
		dir := string(l.Direction)
		if dir == "" {
			dir = string(domain.LinkOutgoing)
		}
		if _, err := tx.Exec(ctx, stmt,
			string(doc.ID), string(l.Target), dir, l.Label); err != nil {
			return upstream(fmt.Sprintf("insert link[%d]", i), err)
		}
	}
	return nil
}

// ExistingSources returns the (source_path → source_commit) map for
// every document whose Source.Repo + Source.Path fall under basePath
// on the given repo. The scanner diffs this against the remote file
// list to compute the new/changed/deleted sets each pass.
func (d *Docs) ExistingSources(ctx context.Context, repo, basePath string) (map[string]string, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT source_path, source_commit
		   FROM documents
		  WHERE source_repo = $1
		    AND source_path LIKE $2`,
		repo, basePath+"%")
	if err != nil {
		return nil, upstream("list sources", err)
	}
	defer rows.Close()

	out := make(map[string]string, 16)
	for rows.Next() {
		var path, sha string
		if err := rows.Scan(&path, &sha); err != nil {
			return nil, upstream("scan source", err)
		}
		out[path] = sha
	}
	if err := rows.Err(); err != nil {
		return nil, upstream("rows sources", err)
	}
	return out, nil
}

// UpsertDiscussion replaces the discussion summary + participants for
// a document in a single transaction. Participants are truncated and
// re-inserted on each call so a force-push-shifted PR thread cannot
// leave stale authors behind (IMPL-0003 Phase 6 force-push handling).
//
// The document must already exist — UpsertDiscussion is called by the
// discussion_fetch handler after the ingest handler has upserted the
// row, so a missing parent is a hard error (surfaces as ErrNotFound
// wrapped by the FK violation).
func (d *Docs) UpsertDiscussion(ctx context.Context, id domain.DocumentID, disc domain.Discussion) error {
	if id == "" {
		return fmt.Errorf("%w: document id is required", domain.ErrInvalidInput)
	}

	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return upstream("begin upsert discussion", err)
	}
	defer func() {
		//nolint:errcheck,gosec // rollback on commit-success is a no-op; on failure the caller already has the cause.
		tx.Rollback(ctx)
	}()

	const stmt = `
		INSERT INTO discussions (document_id, url, comment_count, last_activity, last_synced_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (document_id) DO UPDATE SET
			url            = EXCLUDED.url,
			comment_count  = EXCLUDED.comment_count,
			last_activity  = EXCLUDED.last_activity,
			last_synced_at = EXCLUDED.last_synced_at
	`
	lastActivity := nullIfZero(disc.LastActivity)
	if _, err := tx.Exec(ctx, stmt,
		string(id), nullIfEmpty(disc.URL), disc.CommentCount, lastActivity,
	); err != nil {
		return upstream("upsert discussion", err)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM discussion_participants WHERE document_id = $1`, string(id)); err != nil {
		return upstream("clear participants", err)
	}
	for i, p := range disc.Participants {
		if _, err := tx.Exec(ctx,
			`INSERT INTO discussion_participants (document_id, seq, handle, name, email)
			 VALUES ($1, $2, $3, $4, $5)`,
			string(id), i, p.Handle,
			nullIfEmpty(p.Name), nullIfEmpty(p.Email),
		); err != nil {
			return upstream(fmt.Sprintf("insert participant[%d]", i), err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return upstream("commit upsert discussion", err)
	}
	return nil
}

// nullIfEmpty returns nil for an empty string so the pgx driver
// writes SQL NULL instead of ” — keeps the column semantics aligned
// with the read path (which treats NULL and ” distinctly).
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullIfZero returns nil for a zero time so empty timestamps become
// SQL NULL rather than 0001-01-01.
func nullIfZero(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// Delete removes a document and its cascade (authors, links,
// discussions). Used by the scanner's tombstone path when a source
// file disappears (IMPL-0003 RD4 — hard delete, no tombstones).
func (d *Docs) Delete(ctx context.Context, id domain.DocumentID) error {
	cmd, err := d.pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, string(id))
	if err != nil {
		return upstream("delete document", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}
	return nil
}

func nonZeroTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// getDocument fetches the single documents row. Returns
// domain.ErrNotFound (wrapped) when absent.
func (d *Docs) getDocument(ctx context.Context, id domain.DocumentID) (domain.Document, error) {
	row := d.pool.QueryRow(ctx,
		`SELECT `+documentColumns+` FROM documents WHERE id = $1`,
		string(id))

	var doc domain.Document
	if err := scanDocument(row, &doc); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Document{}, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
		}
		return domain.Document{}, upstream("scan document", err)
	}
	return doc, nil
}

// countDocuments returns the total matching a list query. An empty
// typeIDs slice means cross-type.
func (d *Docs) countDocuments(ctx context.Context, typeIDs []string) (int, error) {
	var total int
	var err error
	if len(typeIDs) == 0 {
		err = d.pool.QueryRow(ctx, `SELECT count(*) FROM documents`).Scan(&total)
	} else {
		err = d.pool.QueryRow(ctx,
			`SELECT count(*) FROM documents WHERE type = ANY($1::text[])`,
			typeIDs).Scan(&total)
	}
	if err != nil {
		return 0, upstream("count documents", err)
	}
	return total, nil
}

// listDocuments runs the paginated SELECT. It over-reads by one row
// so the caller can set NextCursor without a second query.
//
// The query is picked from a switch on (sort, filter present). IMPL-
// 0007 #OQ4 keeps the queries as literal SQL constants rather than a
// templated builder — twelve strings is within tolerable repetition,
// each is paste-into-psql-friendly when debugging, and the existing
// IMPL-0002 style already inlines query strings.
func (d *Docs) listDocuments(ctx context.Context, cfg list.Config) ([]domain.Document, error) {
	// Fetch one extra row so we can decide whether a NextCursor is
	// warranted without re-querying.
	limit := cfg.Limit + 1
	hasFilter := len(cfg.TypeIDs) > 0
	hasCursor := cfg.Cursor != nil

	query, args := buildListQuery(cfg.Sort, hasFilter, hasCursor, cfg, limit)
	rows, err := d.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, upstream("query documents", err)
	}
	defer rows.Close()

	var items []domain.Document
	for rows.Next() {
		var doc domain.Document
		if err := scanDocument(rows, &doc); err != nil {
			return nil, upstream("scan document row", err)
		}
		items = append(items, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, upstream("iterate document rows", err)
	}
	return items, nil
}

// SQL constants for the (sort × filter-present × cursor-present)
// matrix. Twelve queries: 6 sorts × 2 filter states. The cursor's
// keyset comparison varies with sort, so each constant inlines the
// full WHERE shape for clarity at the call site.
//
// Argument order conventions:
//   - $1..$N type-filter array (when present)
//   - $N+1..$N+M cursor keyset values (when present)
//   - $last limit
//
// Time-based sorts encode the cursor's sort column value in $cur1
// and the tiebreaker id in $cur2. Id-based sorts only use $cur1
// for the id tiebreaker. buildListQuery wires the right arg list
// for each (sort, filter, cursor) combination.
const (
	// SortCreatedDesc — today's default ordering.
	qListCreatedDescNoFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		ORDER BY created_at DESC, id ASC LIMIT $1`
	qListCreatedDescNoFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE (created_at < $1) OR (created_at = $1 AND id > $2)
		ORDER BY created_at DESC, id ASC LIMIT $3`
	qListCreatedDescFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[])
		ORDER BY created_at DESC, id ASC LIMIT $2`
	qListCreatedDescFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[])
		  AND ((created_at < $2) OR (created_at = $2 AND id > $3))
		ORDER BY created_at DESC, id ASC LIMIT $4`

	qListCreatedAscNoFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		ORDER BY created_at ASC, id ASC LIMIT $1`
	qListCreatedAscNoFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE (created_at > $1) OR (created_at = $1 AND id > $2)
		ORDER BY created_at ASC, id ASC LIMIT $3`
	qListCreatedAscFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[])
		ORDER BY created_at ASC, id ASC LIMIT $2`
	qListCreatedAscFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[])
		  AND ((created_at > $2) OR (created_at = $2 AND id > $3))
		ORDER BY created_at ASC, id ASC LIMIT $4`

	qListUpdatedDescNoFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		ORDER BY updated_at DESC, id ASC LIMIT $1`
	qListUpdatedDescNoFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE (updated_at < $1) OR (updated_at = $1 AND id > $2)
		ORDER BY updated_at DESC, id ASC LIMIT $3`
	qListUpdatedDescFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[])
		ORDER BY updated_at DESC, id ASC LIMIT $2`
	qListUpdatedDescFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[])
		  AND ((updated_at < $2) OR (updated_at = $2 AND id > $3))
		ORDER BY updated_at DESC, id ASC LIMIT $4`

	qListUpdatedAscNoFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		ORDER BY updated_at ASC, id ASC LIMIT $1`
	qListUpdatedAscNoFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE (updated_at > $1) OR (updated_at = $1 AND id > $2)
		ORDER BY updated_at ASC, id ASC LIMIT $3`
	qListUpdatedAscFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[])
		ORDER BY updated_at ASC, id ASC LIMIT $2`
	qListUpdatedAscFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[])
		  AND ((updated_at > $2) OR (updated_at = $2 AND id > $3))
		ORDER BY updated_at ASC, id ASC LIMIT $4`

	qListIDDescNoFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		ORDER BY id DESC LIMIT $1`
	qListIDDescNoFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE id < $1
		ORDER BY id DESC LIMIT $2`
	qListIDDescFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[])
		ORDER BY id DESC LIMIT $2`
	qListIDDescFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[]) AND id < $2
		ORDER BY id DESC LIMIT $3`

	qListIDAscNoFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		ORDER BY id ASC LIMIT $1`
	qListIDAscNoFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE id > $1
		ORDER BY id ASC LIMIT $2`
	qListIDAscFilterNoCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[])
		ORDER BY id ASC LIMIT $2`
	qListIDAscFilterCursor = `SELECT ` + documentColumns + ` FROM documents
		WHERE type = ANY($1::text[]) AND id > $2
		ORDER BY id ASC LIMIT $3`
)

// buildListQuery dispatches on the active sort to a per-sort helper
// that picks one of four SQL constants based on the (filter, cursor)
// state. Splitting the dispatch keeps each function under the
// gocognit / gocyclo thresholds.
func buildListQuery(s list.Sort, hasFilter, hasCursor bool, cfg list.Config, limit int) (string, []any) {
	switch s {
	case list.SortCreatedDesc:
		return queryCreatedDesc(hasFilter, hasCursor, cfg, limit)
	case list.SortCreatedAsc:
		return queryCreatedAsc(hasFilter, hasCursor, cfg, limit)
	case list.SortUpdatedDesc:
		return queryUpdatedDesc(hasFilter, hasCursor, cfg, limit)
	case list.SortUpdatedAsc:
		return queryUpdatedAsc(hasFilter, hasCursor, cfg, limit)
	case list.SortIDDesc:
		return queryIDDesc(hasFilter, hasCursor, cfg, limit)
	case list.SortIDAsc:
		return queryIDAsc(hasFilter, hasCursor, cfg, limit)
	}
	// Defensive default — list.Apply normalizes the zero sort, but
	// belt-and-suspenders for ordering stability if a future
	// list.Sort enum value lands without a matching case here.
	return qListCreatedDescNoFilterNoCursor, []any{limit}
}

// timeSortArgs builds the positional argument list for a time-based
// sort (created_*, updated_*). The argument order is:
//
//	(filter && cursor): TypeIDs, SortValue, ID, limit
//	(filter && !cursor): TypeIDs, limit
//	(!filter && cursor): SortValue, ID, limit
//	(!filter && !cursor): limit
//
// Returned as a `[]any` ready to hand to pgx.Query.
func timeSortArgs(hasFilter, hasCursor bool, cfg list.Config, limit int) []any {
	switch {
	case !hasFilter && !hasCursor:
		return []any{limit}
	case !hasFilter && hasCursor:
		return []any{cfg.Cursor.SortValue, string(cfg.Cursor.ID), limit}
	case hasFilter && !hasCursor:
		return []any{cfg.TypeIDs, limit}
	default:
		return []any{cfg.TypeIDs, cfg.Cursor.SortValue, string(cfg.Cursor.ID), limit}
	}
}

// idSortArgs is timeSortArgs without the timestamp column. Id-based
// sorts keyset on the id alone.
func idSortArgs(hasFilter, hasCursor bool, cfg list.Config, limit int) []any {
	switch {
	case !hasFilter && !hasCursor:
		return []any{limit}
	case !hasFilter && hasCursor:
		return []any{string(cfg.Cursor.ID), limit}
	case hasFilter && !hasCursor:
		return []any{cfg.TypeIDs, limit}
	default:
		return []any{cfg.TypeIDs, string(cfg.Cursor.ID), limit}
	}
}

func queryCreatedDesc(hasFilter, hasCursor bool, cfg list.Config, limit int) (string, []any) {
	switch {
	case !hasFilter && !hasCursor:
		return qListCreatedDescNoFilterNoCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	case !hasFilter && hasCursor:
		return qListCreatedDescNoFilterCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	case hasFilter && !hasCursor:
		return qListCreatedDescFilterNoCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	default:
		return qListCreatedDescFilterCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	}
}

func queryCreatedAsc(hasFilter, hasCursor bool, cfg list.Config, limit int) (string, []any) {
	switch {
	case !hasFilter && !hasCursor:
		return qListCreatedAscNoFilterNoCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	case !hasFilter && hasCursor:
		return qListCreatedAscNoFilterCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	case hasFilter && !hasCursor:
		return qListCreatedAscFilterNoCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	default:
		return qListCreatedAscFilterCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	}
}

func queryUpdatedDesc(hasFilter, hasCursor bool, cfg list.Config, limit int) (string, []any) {
	switch {
	case !hasFilter && !hasCursor:
		return qListUpdatedDescNoFilterNoCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	case !hasFilter && hasCursor:
		return qListUpdatedDescNoFilterCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	case hasFilter && !hasCursor:
		return qListUpdatedDescFilterNoCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	default:
		return qListUpdatedDescFilterCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	}
}

func queryUpdatedAsc(hasFilter, hasCursor bool, cfg list.Config, limit int) (string, []any) {
	switch {
	case !hasFilter && !hasCursor:
		return qListUpdatedAscNoFilterNoCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	case !hasFilter && hasCursor:
		return qListUpdatedAscNoFilterCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	case hasFilter && !hasCursor:
		return qListUpdatedAscFilterNoCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	default:
		return qListUpdatedAscFilterCursor, timeSortArgs(hasFilter, hasCursor, cfg, limit)
	}
}

func queryIDDesc(hasFilter, hasCursor bool, cfg list.Config, limit int) (string, []any) {
	switch {
	case !hasFilter && !hasCursor:
		return qListIDDescNoFilterNoCursor, idSortArgs(hasFilter, hasCursor, cfg, limit)
	case !hasFilter && hasCursor:
		return qListIDDescNoFilterCursor, idSortArgs(hasFilter, hasCursor, cfg, limit)
	case hasFilter && !hasCursor:
		return qListIDDescFilterNoCursor, idSortArgs(hasFilter, hasCursor, cfg, limit)
	default:
		return qListIDDescFilterCursor, idSortArgs(hasFilter, hasCursor, cfg, limit)
	}
}

func queryIDAsc(hasFilter, hasCursor bool, cfg list.Config, limit int) (string, []any) {
	switch {
	case !hasFilter && !hasCursor:
		return qListIDAscNoFilterNoCursor, idSortArgs(hasFilter, hasCursor, cfg, limit)
	case !hasFilter && hasCursor:
		return qListIDAscNoFilterCursor, idSortArgs(hasFilter, hasCursor, cfg, limit)
	case hasFilter && !hasCursor:
		return qListIDAscFilterNoCursor, idSortArgs(hasFilter, hasCursor, cfg, limit)
	default:
		return qListIDAscFilterCursor, idSortArgs(hasFilter, hasCursor, cfg, limit)
	}
}

// discussionOrNil returns a *Discussion (without Participants) or nil
// when no discussion row exists. Get() uses this to populate
// Document.Discussion; Discussion() uses it and then loads
// participants.
func (d *Docs) discussionOrNil(ctx context.Context, id domain.DocumentID) (*domain.Discussion, error) {
	row := d.pool.QueryRow(ctx,
		`SELECT url, comment_count, last_activity
		   FROM discussions
		  WHERE document_id = $1`, string(id))

	var (
		url          *string
		commentCount int
		lastActivity *time.Time
	)
	switch err := row.Scan(&url, &commentCount, &lastActivity); {
	case errors.Is(err, pgx.ErrNoRows):
		// Absent discussion is not an error — Get() treats nil as
		// "no discussion was seeded" and leaves Document.Discussion
		// as-is. A sentinel would force every call site to check
		// for a specific error that means "this is fine", which
		// obscures the read path.
		return nil, nil //nolint:nilnil // nil,nil = "no discussion row, not an error"
	case err != nil:
		return nil, upstream("scan discussion", err)
	}

	disc := &domain.Discussion{CommentCount: commentCount}
	if url != nil {
		disc.URL = *url
	}
	if lastActivity != nil {
		disc.LastActivity = *lastActivity
	}
	return disc, nil
}

// discussionParticipants returns the participants list in seq order.
func (d *Docs) discussionParticipants(ctx context.Context, id domain.DocumentID) ([]domain.Author, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT handle, name, email
		  FROM discussion_participants
		 WHERE document_id = $1
		 ORDER BY seq`, string(id))
	if err != nil {
		return nil, upstream("query participants", err)
	}
	defer rows.Close()

	var out []domain.Author
	for rows.Next() {
		var a domain.Author
		var name, email *string
		if err := rows.Scan(&a.Handle, &name, &email); err != nil {
			return nil, upstream("scan participant row", err)
		}
		if name != nil {
			a.Name = *name
		}
		if email != nil {
			a.Email = *email
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, upstream("iterate participant rows", err)
	}
	return out, nil
}

// ensureExists returns domain.ErrNotFound when id is absent from the
// documents table. Used by sub-resource endpoints so they 404 with
// the same envelope the Get endpoint would.
func (d *Docs) ensureExists(ctx context.Context, id domain.DocumentID) error {
	var found bool
	err := d.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM documents WHERE id = $1)`,
		string(id)).Scan(&found)
	if err != nil {
		return upstream("check document existence", err)
	}
	if !found {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}
	return nil
}

// scanDocument copies a documents-row into the given Document. Kept
// as a package-level helper so both QueryRow and Query.Rows callers
// can share the scan target layout.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDocument(row rowScanner, doc *domain.Document) error {
	var (
		status        *string
		body          *string
		sourceRepo    *string
		sourcePath    *string
		sourceCommit  *string
		id, typeID    string
		labels        []string
		extensionsRaw map[string]any
	)
	err := row.Scan(
		&id, &typeID, &doc.Title, &status, &body,
		&doc.CreatedAt, &doc.UpdatedAt,
		&labels, &extensionsRaw,
		&sourceRepo, &sourcePath, &sourceCommit,
	)
	if err != nil {
		return err
	}

	doc.ID = domain.DocumentID(id)
	doc.Type = typeID
	doc.Labels = labels
	doc.Extensions = extensionsRaw
	if status != nil {
		doc.Status = *status
	}
	if body != nil {
		doc.Body = *body
	}
	if sourceRepo != nil {
		doc.Source.Repo = *sourceRepo
	}
	if sourcePath != nil {
		doc.Source.Path = *sourcePath
	}
	if sourceCommit != nil {
		doc.Source.Commit = *sourceCommit
	}
	return nil
}

// upstream wraps a driver error with context and marks it as a
// domain.ErrUpstream failure. Reserving ErrNotFound for the
// single-row pgx.ErrNoRows case is the caller's responsibility.
func upstream(what string, err error) error {
	return fmt.Errorf("%s: %w: %w", what, domain.ErrUpstream, err)
}
