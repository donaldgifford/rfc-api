package memory_test

import (
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store"
	"github.com/donaldgifford/rfc-api/internal/store/memory"
)

func seedFS() fstest.MapFS {
	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	d1 := `{
		"id":"RFC-0001","type":"rfc","title":"first","status":"Draft",
		"created_at":"` + now.Add(-2*time.Hour).Format(time.RFC3339Nano) + `",
		"updated_at":"` + now.Format(time.RFC3339Nano) + `",
		"source":{"repo":"x/x","path":"docs/rfc/0001.md"},
		"authors":[{"name":"Ada"}]
	}`
	d2 := `{
		"id":"ADR-0001","type":"adr","title":"adr-first","status":"Accepted",
		"created_at":"` + now.Add(-1*time.Hour).Format(time.RFC3339Nano) + `",
		"updated_at":"` + now.Format(time.RFC3339Nano) + `",
		"source":{"repo":"x/x","path":"docs/adr/0001.md"},
		"links":[{"direction":"outgoing","target":"RFC-0001","href":"/api/v1/rfc/0001"}]
	}`
	d3 := `{
		"id":"RFC-0002","type":"rfc","title":"second","status":"Proposed",
		"created_at":"` + now.Format(time.RFC3339Nano) + `",
		"updated_at":"` + now.Format(time.RFC3339Nano) + `",
		"source":{"repo":"x/x","path":"docs/rfc/0002.md"}
	}`
	return fstest.MapFS{
		"seed/rfc-0001.json": {Data: []byte(d1)},
		"seed/adr-0001.json": {Data: []byte(d2)},
		"seed/rfc-0002.json": {Data: []byte(d3)},
		"seed/not-json.txt":  {Data: []byte("ignored")},
	}
}

func TestLoadDir(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	doc, err := s.Get(t.Context(), "RFC-0001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if doc.Title != "first" {
		t.Errorf("Title = %q, want first", doc.Title)
	}
}

func TestGet_NotFound(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(t.Context(), "RFC-9999")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
}

func TestList_CrossType(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.List(t.Context(), store.ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("want 3 items, total 3; got len=%d total=%d", len(page.Items), page.Total)
	}
	// CreatedAt DESC sort — RFC-0002 (newest) is first.
	if page.Items[0].ID != "RFC-0002" {
		t.Errorf("first item = %q, want RFC-0002", page.Items[0].ID)
	}
}

func TestList_ByType(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.List(t.Context(), store.ListQuery{TypeID: "rfc", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Errorf("Total = %d, want 2", page.Total)
	}
	for _, d := range page.Items {
		if d.Type != "rfc" {
			t.Errorf("got non-rfc item: %+v", d)
		}
	}
}

func TestList_PaginationCursor(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.List(t.Context(), store.ListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == nil {
		t.Fatal("want NextCursor on first page")
	}
	if len(first.Items) != 2 {
		t.Fatalf("first page len = %d, want 2", len(first.Items))
	}

	second, err := s.List(t.Context(), store.ListQuery{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if second.NextCursor != nil {
		t.Errorf("want nil NextCursor on final page, got %+v", second.NextCursor)
	}
	if len(second.Items) != 1 {
		t.Fatalf("second page len = %d, want 1", len(second.Items))
	}
	// No overlap between pages.
	if second.Items[0].ID == first.Items[0].ID || second.Items[0].ID == first.Items[1].ID {
		t.Errorf("page overlap: first=%v second=%v", idsOf(first.Items), idsOf(second.Items))
	}
}

func TestList_BadLimit(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.List(t.Context(), store.ListQuery{Limit: 0})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("want ErrInvalidInput, got %v", err)
	}
}

func TestLinks_OutgoingAndIncoming(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Links(t.Context(), "ADR-0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Direction != domain.LinkOutgoing || out[0].Target != "RFC-0001" {
		t.Errorf("outgoing links from ADR-0001 = %+v", out)
	}

	in, err := s.Links(t.Context(), "RFC-0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].Direction != domain.LinkIncoming || in[0].Target != "ADR-0001" {
		t.Errorf("incoming links to RFC-0001 = %+v", in)
	}
}

func TestAuthors(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	authors, err := s.Authors(t.Context(), "RFC-0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || authors[0].Name != "Ada" {
		t.Errorf("authors = %+v", authors)
	}
}

func TestRevisionsStub(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	revs, err := s.Revisions(t.Context(), "RFC-0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 1 {
		t.Fatalf("want 1 stub revision, got %d", len(revs))
	}
}

func TestDiscussion_Empty(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Discussion(t.Context(), "RFC-0001")
	if err != nil {
		t.Fatal(err)
	}
	if d.CommentCount != 0 {
		t.Errorf("default Discussion should be zero-value, got %+v", d)
	}
}

func TestAdd_DuplicateID(t *testing.T) {
	s := memory.New()
	doc := &domain.Document{ID: "RFC-0001", Type: "rfc", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.Add(doc); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(doc); err == nil {
		t.Errorf("want duplicate id error")
	}
}

func idsOf(items []domain.Document) []domain.DocumentID {
	out := make([]domain.DocumentID, len(items))
	for i := range items {
		out[i] = items[i].ID
	}
	return out
}
