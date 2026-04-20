//go:build integration

package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store/postgres"
)

func TestUpsert_Insert(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	doc := sampleDoc("RFC-0100", "rfc", time.Now().UTC().Truncate(time.Microsecond))
	doc.Links = []domain.Link{
		{Direction: domain.LinkOutgoing, Target: "RFC-0001", TargetURL: "/api/v1/rfc/0001", Label: "baseline"},
	}

	if err := docs.Upsert(t.Context(), &doc); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got := mustGet(t, docs, doc.ID)
	if got.Title != doc.Title {
		t.Errorf("title = %q, want %q", got.Title, doc.Title)
	}

	// Authors + links arrive via their own fetches.
	links, err := docs.Links(t.Context(), doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Target != "RFC-0001" {
		t.Errorf("links = %+v", links)
	}
}

func TestUpsert_PreservesCreatedAt(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	orig := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	doc := sampleDoc("RFC-0200", "rfc", orig)
	if err := docs.Upsert(t.Context(), &doc); err != nil {
		t.Fatal(err)
	}
	// Re-upsert with a newer CreatedAt — store should preserve the
	// original per DESIGN-0001 archival semantics.
	doc.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	doc.Title = "updated"
	if err := docs.Upsert(t.Context(), &doc); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, docs, doc.ID)
	if !got.CreatedAt.Equal(orig) {
		t.Errorf("CreatedAt = %v, want %v (preserved)", got.CreatedAt, orig)
	}
	if got.Title != "updated" {
		t.Errorf("Title = %q, want updated", got.Title)
	}
}

func TestUpsert_ReplacesAuthorsAndLinks(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	doc := sampleDoc("RFC-0300", "rfc", time.Now().UTC().Truncate(time.Microsecond))
	doc.Authors = []domain.Author{{Name: "Alice"}, {Name: "Bob"}}
	doc.Links = []domain.Link{
		{Direction: domain.LinkOutgoing, Target: "RFC-0001", TargetURL: "/api/v1/rfc/0001", Label: "old"},
	}
	if err := docs.Upsert(t.Context(), &doc); err != nil {
		t.Fatal(err)
	}

	// Second upsert drops Bob and swaps the link.
	doc.Authors = []domain.Author{{Name: "Alice"}}
	doc.Links = []domain.Link{
		{Direction: domain.LinkOutgoing, Target: "RFC-0002", TargetURL: "/api/v1/rfc/0002", Label: "new"},
	}
	if err := docs.Upsert(t.Context(), &doc); err != nil {
		t.Fatal(err)
	}

	got, err := docs.Authors(t.Context(), doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Alice" {
		t.Errorf("authors = %+v", got)
	}
	links, _ := docs.Links(t.Context(), doc.ID)
	if len(links) != 1 || links[0].Target != "RFC-0002" {
		t.Errorf("links = %+v", links)
	}
}

func TestDelete_Cascades(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	doc := sampleDoc("RFC-0400", "rfc", time.Now().UTC().Truncate(time.Microsecond))
	doc.Authors = []domain.Author{{Name: "Ada"}}
	doc.Links = []domain.Link{
		{Direction: domain.LinkOutgoing, Target: "RFC-0001", TargetURL: "/api/v1/rfc/0001", Label: "x"},
	}
	if err := docs.Upsert(t.Context(), &doc); err != nil {
		t.Fatal(err)
	}
	if err := docs.Delete(t.Context(), doc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Get(t.Context(), doc.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("want ErrNotFound after delete, got %v", err)
	}
}

func TestExistingSources_FiltersByRepoAndBase(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	base := time.Now().UTC().Truncate(time.Microsecond)
	for i, id := range []string{"RFC-0001", "RFC-0002"} {
		d := sampleDoc(id, "rfc", base)
		d.Source = domain.Source{Repo: "owner/same", Path: "docs/rfc/" + id + ".md", Commit: "sha" + id}
		if err := docs.Upsert(t.Context(), &d); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	// Different repo — must not appear in the map.
	other := sampleDoc("RFC-0003", "rfc", base)
	other.Source = domain.Source{Repo: "owner/other", Path: "docs/rfc/RFC-0003.md", Commit: "sha3"}
	if err := docs.Upsert(t.Context(), &other); err != nil {
		t.Fatal(err)
	}

	got, err := docs.ExistingSources(t.Context(), "owner/same", "docs/rfc/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(got), got)
	}
	if got["docs/rfc/RFC-0001.md"] != "shaRFC-0001" {
		t.Errorf("unexpected sha: %+v", got)
	}
}
