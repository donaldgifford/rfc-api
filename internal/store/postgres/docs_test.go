//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store"
	"github.com/donaldgifford/rfc-api/internal/store/postgres"
)

// testPool opens a pool against DATABASE_URL and resets the public
// schema so each test starts from a known-clean slate. Returns the
// pool and a cleanup closure.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := postgres.NewPool(t.Context(), dsn, logger)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	// Every test runs against a migrated schema but with all rows
	// deleted — faster than drop+recreate and enough isolation for
	// a single-serial test process.
	truncate(t, pool)
	t.Cleanup(func() { truncate(t, pool) })
	return pool
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`TRUNCATE TABLE
			discussion_participants,
			discussions,
			links,
			authors,
			documents,
			jobs
		 RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// insertDoc seeds one document with its authors (optional) and
// returns the stored id. Kept inline so the tests document the
// insert contract alongside what's asserted.
func insertDoc(t *testing.T, pool *pgxpool.Pool, doc domain.Document) {
	t.Helper()
	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		INSERT INTO documents (id, type, title, status, body,
		                      created_at, updated_at, labels, extensions,
		                      source_repo, source_path, source_commit)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		string(doc.ID), doc.Type, doc.Title, doc.Status, doc.Body,
		doc.CreatedAt, doc.UpdatedAt, doc.Labels, doc.Extensions,
		doc.Source.Repo, doc.Source.Path, doc.Source.Commit)
	if err != nil {
		t.Fatalf("insert documents: %v", err)
	}

	for i, a := range doc.Authors {
		_, err := pool.Exec(ctx,
			`INSERT INTO authors (document_id, seq, name, email, handle)
			 VALUES ($1, $2, $3, $4, $5)`,
			string(doc.ID), i, a.Name, a.Email, a.Handle)
		if err != nil {
			t.Fatalf("insert authors: %v", err)
		}
	}
}

func mustGet(t *testing.T, s store.Docs, id domain.DocumentID) domain.Document {
	t.Helper()
	doc, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get(%q): %v", id, err)
	}
	return doc
}

// sampleDoc returns a filled-in Document suitable for insertDoc.
// Fields like Labels and Extensions exercise the array/jsonb paths.
func sampleDoc(id, typeID string, created time.Time) domain.Document {
	return domain.Document{
		ID:         domain.DocumentID(id),
		Type:       typeID,
		Title:      "Sample " + id,
		Status:     "Draft",
		Body:       "# Hello\n",
		CreatedAt:  created,
		UpdatedAt:  created,
		Labels:     []string{"alpha", "beta"},
		Extensions: map[string]any{"priority": "high"},
		Source: domain.Source{
			Repo:   "donaldgifford/rfc-repo",
			Path:   "docs/" + typeID + "/" + id + ".md",
			Commit: "deadbeef",
		},
		Authors: []domain.Author{
			{Name: "Donald Gifford", Handle: "@dgifford"},
		},
	}
}

func TestDocs_GetRoundtrip(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	seed := sampleDoc("RFC-0001", "rfc", time.Now().UTC().Truncate(time.Microsecond))
	insertDoc(t, pool, seed)

	got := mustGet(t, docs, seed.ID)

	if got.ID != seed.ID {
		t.Errorf("id = %q, want %q", got.ID, seed.ID)
	}
	if got.Title != seed.Title {
		t.Errorf("title = %q, want %q", got.Title, seed.Title)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "alpha" {
		t.Errorf("labels = %v, want [alpha beta]", got.Labels)
	}
	if got.Extensions["priority"] != "high" {
		t.Errorf("extensions.priority = %v, want high", got.Extensions["priority"])
	}
	if len(got.Authors) != 1 || got.Authors[0].Handle != "@dgifford" {
		t.Errorf("authors = %#v", got.Authors)
	}
}

func TestDocs_GetMissing_Returns_NotFound(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	_, err := docs.Get(t.Context(), "RFC-9999")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping ErrNotFound", err)
	}
}

func TestDocs_List_RespectsTypeFilter(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	base := time.Now().UTC().Truncate(time.Microsecond)
	insertDoc(t, pool, sampleDoc("RFC-0001", "rfc", base))
	insertDoc(t, pool, sampleDoc("ADR-0001", "adr", base.Add(-time.Minute)))

	page, err := docs.List(t.Context(), store.ListQuery{TypeID: "rfc", Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "RFC-0001" {
		t.Fatalf("items = %#v", page.Items)
	}
	if page.Total != 1 {
		t.Errorf("total = %d, want 1", page.Total)
	}
}

func TestDocs_List_KeysetPaginationStable(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	// Insert three RFCs at descending timestamps so the expected
	// order is RFC-0003, RFC-0002, RFC-0001.
	base := time.Now().UTC().Truncate(time.Microsecond)
	insertDoc(t, pool, sampleDoc("RFC-0001", "rfc", base.Add(-2*time.Minute)))
	insertDoc(t, pool, sampleDoc("RFC-0002", "rfc", base.Add(-1*time.Minute)))
	insertDoc(t, pool, sampleDoc("RFC-0003", "rfc", base))

	// First page limit=2 should return RFC-0003, RFC-0002 plus a
	// NextCursor pointing at RFC-0002.
	p1, err := docs.List(t.Context(), store.ListQuery{TypeID: "rfc", Limit: 2})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(p1.Items) != 2 {
		t.Fatalf("page 1 items = %d, want 2", len(p1.Items))
	}
	if p1.Items[0].ID != "RFC-0003" || p1.Items[1].ID != "RFC-0002" {
		t.Fatalf("page 1 order = %#v", []domain.DocumentID{p1.Items[0].ID, p1.Items[1].ID})
	}
	if p1.NextCursor == nil {
		t.Fatal("page 1 NextCursor is nil, want non-nil")
	}
	if p1.Total != 3 {
		t.Errorf("total = %d, want 3", p1.Total)
	}

	// Mid-pagination insert. A keyset cursor must not cause
	// subsequent pages to skip or duplicate — the new row should
	// only appear if its created_at falls within the remaining
	// page, which it doesn't here (it's older than RFC-0002).
	insertDoc(t, pool, sampleDoc("RFC-0004", "rfc", base.Add(-90*time.Second)))

	// Second page using the cursor from page 1 should return
	// RFC-0001 (and NOT RFC-0002, confirming we skipped past it).
	p2, err := docs.List(t.Context(), store.ListQuery{
		TypeID: "rfc", Limit: 2, Cursor: p1.NextCursor,
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(p2.Items) != 2 {
		t.Fatalf("page 2 items = %d, want 2 (RFC-0004 + RFC-0001)", len(p2.Items))
	}
	if p2.Items[0].ID != "RFC-0004" || p2.Items[1].ID != "RFC-0001" {
		t.Fatalf("page 2 order = %#v", []domain.DocumentID{p2.Items[0].ID, p2.Items[1].ID})
	}
	if p2.NextCursor != nil {
		t.Errorf("page 2 NextCursor = %#v, want nil (last page)", p2.NextCursor)
	}
}

func TestDocs_Links_IncomingAndOutgoing(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	base := time.Now().UTC().Truncate(time.Microsecond)
	insertDoc(t, pool, sampleDoc("RFC-0001", "rfc", base))
	insertDoc(t, pool, sampleDoc("RFC-0002", "rfc", base.Add(-time.Minute)))

	// 0002 → 0001 (outgoing from 0002 / incoming to 0001)
	_, err := pool.Exec(t.Context(), `
		INSERT INTO links (source_id, target_id, direction, label)
		VALUES ('RFC-0002', 'RFC-0001', 'outgoing', 'references')`)
	if err != nil {
		t.Fatalf("seed links: %v", err)
	}

	links, err := docs.Links(t.Context(), "RFC-0001")
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("links = %#v", links)
	}
	if links[0].Direction != domain.LinkIncoming {
		t.Errorf("direction = %q, want incoming", links[0].Direction)
	}
	if links[0].Target != "RFC-0002" {
		t.Errorf("target = %q, want RFC-0002", links[0].Target)
	}
}

func TestDocs_Discussion_PopulatesParticipants(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	base := time.Now().UTC().Truncate(time.Microsecond)
	insertDoc(t, pool, sampleDoc("RFC-0001", "rfc", base))

	_, err := pool.Exec(t.Context(), `
		INSERT INTO discussions (document_id, url, comment_count, last_activity)
		VALUES ('RFC-0001', 'https://github.com/foo/bar/pull/42', 3, now())`)
	if err != nil {
		t.Fatalf("seed discussions: %v", err)
	}
	_, err = pool.Exec(t.Context(), `
		INSERT INTO discussion_participants (document_id, seq, handle, name)
		VALUES ('RFC-0001', 0, 'alice', 'Alice'),
		       ('RFC-0001', 1, 'bob',   'Bob')`)
	if err != nil {
		t.Fatalf("seed participants: %v", err)
	}

	disc, err := docs.Discussion(t.Context(), "RFC-0001")
	if err != nil {
		t.Fatalf("Discussion: %v", err)
	}
	if disc.CommentCount != 3 {
		t.Errorf("comment_count = %d, want 3", disc.CommentCount)
	}
	if len(disc.Participants) != 2 {
		t.Fatalf("participants = %#v", disc.Participants)
	}
	if disc.Participants[0].Handle != "alice" {
		t.Errorf("participant[0].handle = %q, want alice", disc.Participants[0].Handle)
	}
}

func TestDocs_Revisions_StubReturnsEmpty(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	base := time.Now().UTC().Truncate(time.Microsecond)
	insertDoc(t, pool, sampleDoc("RFC-0001", "rfc", base))

	revs, err := docs.Revisions(t.Context(), "RFC-0001")
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revs) != 0 {
		t.Errorf("revisions = %#v, want empty slice", revs)
	}

	_, err = docs.Revisions(t.Context(), "RFC-9999")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing-doc revisions err = %v, want ErrNotFound", err)
	}
}

func TestDocs_Upsert_StubReturnsError(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	err := docs.Upsert(t.Context(), &domain.Document{ID: "RFC-0001"})
	if err == nil {
		t.Fatal("Upsert stub returned nil, want error")
	}
}
