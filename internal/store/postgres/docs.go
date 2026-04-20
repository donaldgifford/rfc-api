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

// List implements store.Docs.List with keyset pagination on
// (created_at DESC, id ASC). The type filter is a no-op when
// q.TypeID is empty — the cross-type /api/v1/docs surface.
//
// List returns only the document-level columns. Callers that need
// authors / links / discussion hit the dedicated sub-resource
// endpoints (which run through those store methods).
func (d *Docs) List(ctx context.Context, q store.ListQuery) (store.Page, error) {
	if q.Limit <= 0 {
		return store.Page{}, fmt.Errorf("%w: limit must be positive", domain.ErrInvalidInput)
	}

	total, err := d.countDocuments(ctx, q.TypeID)
	if err != nil {
		return store.Page{}, err
	}

	items, err := d.listDocuments(ctx, q)
	if err != nil {
		return store.Page{}, err
	}

	page := store.Page{Total: total}
	if len(items) > q.Limit {
		last := items[q.Limit-1]
		page.NextCursor = &store.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
		page.Items = items[:q.Limit]
	} else {
		page.Items = items
	}
	return page, nil
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
func (*Docs) Upsert(_ context.Context, doc *domain.Document) error {
	return fmt.Errorf(
		"upsert %s: not implemented in IMPL-0002; worker write path lands in IMPL-0003",
		doc.ID,
	)
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

// countDocuments returns the total matching a list query. typeID ""
// means cross-type.
func (d *Docs) countDocuments(ctx context.Context, typeID string) (int, error) {
	var total int
	var err error
	if typeID == "" {
		err = d.pool.QueryRow(ctx, `SELECT count(*) FROM documents`).Scan(&total)
	} else {
		err = d.pool.QueryRow(ctx,
			`SELECT count(*) FROM documents WHERE type = $1`,
			typeID).Scan(&total)
	}
	if err != nil {
		return 0, upstream("count documents", err)
	}
	return total, nil
}

// listDocuments runs the paginated SELECT. It over-reads by one row
// so the caller can set NextCursor without a second query.
func (d *Docs) listDocuments(ctx context.Context, q store.ListQuery) ([]domain.Document, error) {
	// Fetch one extra row so we can decide whether a NextCursor is
	// warranted without re-querying.
	limit := q.Limit + 1

	var (
		rows pgx.Rows
		err  error
	)
	switch {
	case q.TypeID == "" && q.Cursor == nil:
		rows, err = d.pool.Query(ctx,
			`SELECT `+documentColumns+` FROM documents
			   ORDER BY created_at DESC, id ASC
			   LIMIT $1`, limit)
	case q.TypeID == "" && q.Cursor != nil:
		rows, err = d.pool.Query(ctx,
			`SELECT `+documentColumns+` FROM documents
			  WHERE (created_at < $1)
			     OR (created_at = $1 AND id > $2)
			   ORDER BY created_at DESC, id ASC
			   LIMIT $3`,
			q.Cursor.CreatedAt, string(q.Cursor.ID), limit)
	case q.TypeID != "" && q.Cursor == nil:
		rows, err = d.pool.Query(ctx,
			`SELECT `+documentColumns+` FROM documents
			  WHERE type = $1
			   ORDER BY created_at DESC, id ASC
			   LIMIT $2`, q.TypeID, limit)
	default: // typeID != "" && cursor != nil
		rows, err = d.pool.Query(ctx,
			`SELECT `+documentColumns+` FROM documents
			  WHERE type = $1
			    AND ((created_at < $2)
			      OR (created_at = $2 AND id > $3))
			   ORDER BY created_at DESC, id ASC
			   LIMIT $4`,
			q.TypeID, q.Cursor.CreatedAt, string(q.Cursor.ID), limit)
	}
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
