package memory_test

import (
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store/list"
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
	page, err := s.List(t.Context(), list.WithLimit(10))
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
	page, err := s.List(t.Context(), list.WithTypes("rfc"), list.WithLimit(10))
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
	first, err := s.List(t.Context(), list.WithLimit(2))
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == nil {
		t.Fatal("want NextCursor on first page")
	}
	if len(first.Items) != 2 {
		t.Fatalf("first page len = %d, want 2", len(first.Items))
	}

	second, err := s.List(t.Context(), list.WithLimit(2), list.WithCursor(first.NextCursor))
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
	_, err = s.List(t.Context(), list.WithLimit(0))
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("want ErrInvalidInput, got %v", err)
	}
}

// TestList_SortVariants verifies the memory store dispatches on every
// list.Sort value documented in DESIGN-0003 #Sort-semantics and emits
// the expected order. The seed has 3 docs with distinct created_at
// times; updated_at is the same across all three so the id tiebreaker
// drives the updated_* cases.
func TestList_SortVariants(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		sort list.Sort
		want []domain.DocumentID
	}{
		// created_desc: newest first = RFC-0002 → ADR-0001 → RFC-0001
		{
			"created_desc", list.SortCreatedDesc,
			[]domain.DocumentID{"RFC-0002", "ADR-0001", "RFC-0001"},
		},
		// created_asc: oldest first
		{
			"created_asc", list.SortCreatedAsc,
			[]domain.DocumentID{"RFC-0001", "ADR-0001", "RFC-0002"},
		},
		// id_asc: alphabetical
		{
			"id_asc", list.SortIDAsc,
			[]domain.DocumentID{"ADR-0001", "RFC-0001", "RFC-0002"},
		},
		// id_desc: reverse alphabetical
		{
			"id_desc", list.SortIDDesc,
			[]domain.DocumentID{"RFC-0002", "RFC-0001", "ADR-0001"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page, err := s.List(t.Context(), list.WithSort(c.sort), list.WithLimit(10))
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(page.Items) != len(c.want) {
				t.Fatalf("got %d items, want %d", len(page.Items), len(c.want))
			}
			for i, w := range c.want {
				if page.Items[i].ID != w {
					t.Errorf("position %d: got %q, want %q", i, page.Items[i].ID, w)
				}
			}
		})
	}
}

// TestList_FilterOR_AcrossMultipleTypes pins the OR-within-field
// semantics from DESIGN-0003 #Filter-semantics. Asking for type:rfc
// OR type:adr should return all three seed docs.
func TestList_FilterOR_AcrossMultipleTypes(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.List(
		t.Context(),
		list.WithTypes("rfc", "adr"),
		list.WithLimit(10),
	)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 {
		t.Errorf("Total = %d, want 3 (rfc:2 + adr:1)", page.Total)
	}
	for _, d := range page.Items {
		if d.Type != "rfc" && d.Type != "adr" {
			t.Errorf("unexpected type in OR filter result: %q", d.Type)
		}
	}
}

// TestList_CursorMatchesSort verifies that NextCursor is minted with
// the request's active sort, so the handler-layer cross-check can
// compare cursor.Sort vs request.Sort.
func TestList_CursorMatchesSort(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.List(
		t.Context(),
		list.WithSort(list.SortIDAsc),
		list.WithLimit(2),
	)
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == nil {
		t.Fatal("want NextCursor for partial page")
	}
	if page.NextCursor.Sort != list.SortIDAsc {
		t.Errorf("NextCursor.Sort = %q, want %q", page.NextCursor.Sort, list.SortIDAsc)
	}
}

// TestList_PaginationUnderSort verifies cursor traversal works
// correctly under a non-default sort (id_asc), exercising the
// rowAfterCursor dispatch.
func TestList_PaginationUnderSort(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	p1, err := s.List(
		t.Context(),
		list.WithSort(list.SortIDAsc),
		list.WithLimit(1),
	)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Items[0].ID != "ADR-0001" {
		t.Fatalf("page1[0] = %q, want ADR-0001", p1.Items[0].ID)
	}
	p2, err := s.List(
		t.Context(),
		list.WithSort(list.SortIDAsc),
		list.WithLimit(2),
		list.WithCursor(p1.NextCursor),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.DocumentID{"RFC-0001", "RFC-0002"}
	if len(p2.Items) != 2 {
		t.Fatalf("page2 len = %d, want 2", len(p2.Items))
	}
	for i, w := range want {
		if p2.Items[i].ID != w {
			t.Errorf("page2[%d] = %q, want %q", i, p2.Items[i].ID, w)
		}
	}
}

// TestCountAll_UnfilteredTotal proves CountAll ignores any options —
// the handler relies on this for X-Total-Count-Unfiltered when a
// filter is active.
func TestCountAll_UnfilteredTotal(t *testing.T) {
	s, err := memory.LoadDir(seedFS(), "seed")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CountAll(t.Context())
	if err != nil {
		t.Fatalf("CountAll: %v", err)
	}
	if got != 3 {
		t.Errorf("CountAll = %d, want 3 (all seed docs regardless of filter)", got)
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
