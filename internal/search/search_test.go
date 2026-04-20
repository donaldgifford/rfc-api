package search_test

import (
	"testing"

	"github.com/donaldgifford/rfc-api/internal/search"
)

func TestNoopClient_Query(t *testing.T) {
	page, err := search.NoopClient{}.Query(t.Context(), search.Query{Q: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 0 || page.Total != 0 || page.NextCursor != "" {
		t.Errorf("noop should be zero, got %+v", page)
	}
}
