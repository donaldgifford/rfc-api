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

func TestUpsertDiscussion_Roundtrip(t *testing.T) {
	pool := testPool(t)
	docs := postgres.NewDocs(pool)

	doc := sampleDoc("RFC-0500", "rfc", time.Now().UTC().Truncate(time.Microsecond))
	if err := docs.Upsert(t.Context(), &doc); err != nil {
		t.Fatal(err)
	}

	activity := time.Now().UTC().Truncate(time.Microsecond)
	disc := domain.Discussion{
		URL:          "https://github.com/example/repo/pull/42",
		CommentCount: 3,
		LastActivity: activity,
		Participants: []domain.Author{
			{Handle: "alice", Name: "Alice"},
			{Handle: "bob"},
		},
	}
	if err := docs.UpsertDiscussion(t.Context(), doc.ID, disc); err != nil {
		t.Fatalf("UpsertDiscussion: %v", err)
	}

	got, err := docs.Discussion(t.Context(), doc.ID)
	if err != nil {
		t.Fatalf("Discussion: %v", err)
	}
	if got.URL != disc.URL || got.CommentCount != 3 {
		t.Errorf("got %+v", got)
	}
	if len(got.Participants) != 2 || got.Participants[0].Handle != "alice" {
		t.Errorf("participants = %+v", got.Participants)
	}

	// Re-upsert with fewer participants to prove the delete+reinsert
	// handles force-push-style history rewrites.
	disc2 := disc
	disc2.Participants = []domain.Author{{Handle: "carol"}}
	disc2.CommentCount = 1
	if err := docs.UpsertDiscussion(t.Context(), doc.ID, disc2); err != nil {
		t.Fatalf("re-UpsertDiscussion: %v", err)
	}
	got2, _ := docs.Discussion(t.Context(), doc.ID)
	if len(got2.Participants) != 1 || got2.Participants[0].Handle != "carol" {
		t.Errorf("participants after rewrite = %+v", got2.Participants)
	}
	if got2.CommentCount != 1 {
		t.Errorf("count after rewrite = %d", got2.CommentCount)
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
