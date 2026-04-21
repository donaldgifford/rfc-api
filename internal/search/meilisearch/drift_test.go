package meilisearch_test

import (
	"testing"

	"github.com/donaldgifford/rfc-api/internal/search/meilisearch"
)

func TestCompareDrift_ZeroWhenAligned(t *testing.T) {
	got := meilisearch.CompareDrift(
		map[string]int{"rfc": 3, "adr": 1},
		map[string]int{"rfc": 3, "adr": 1},
	)
	for _, r := range got {
		if r.Delta != 0 {
			t.Errorf("drift in aligned snapshot: %+v", r)
		}
	}
}

func TestCompareDrift_MissingOnMeili(t *testing.T) {
	got := meilisearch.CompareDrift(
		map[string]int{"rfc": 5},
		map[string]int{"rfc": 2},
	)
	if len(got) != 1 || got[0].Delta != 3 {
		t.Errorf("got %+v, want one +3 drift for rfc", got)
	}
}

func TestCompareDrift_ExtraOnMeili(t *testing.T) {
	// A tombstone in Postgres that Meili hasn't caught up on.
	got := meilisearch.CompareDrift(
		map[string]int{"rfc": 1},
		map[string]int{"rfc": 3},
	)
	if got[0].Delta != -2 {
		t.Errorf("delta = %d, want -2", got[0].Delta)
	}
}

func TestCompareDrift_OrderedByType(t *testing.T) {
	got := meilisearch.CompareDrift(
		map[string]int{"rfc": 1, "adr": 1, "zzz": 1},
		map[string]int{},
	)
	if got[0].Type != "adr" || got[1].Type != "rfc" || got[2].Type != "zzz" {
		t.Errorf("order = %+v, want adr, rfc, zzz", got)
	}
}
