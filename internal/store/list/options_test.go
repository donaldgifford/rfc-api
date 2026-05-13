package list

import (
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
)

func TestApply_NoOptionsReturnsDefaults(t *testing.T) {
	t.Parallel()
	cfg := Apply()
	if cfg.Sort != DefaultSort {
		t.Errorf("Sort = %q, want default %q", cfg.Sort, DefaultSort)
	}
	if len(cfg.TypeIDs) != 0 {
		t.Errorf("TypeIDs = %v, want empty", cfg.TypeIDs)
	}
	if cfg.Limit != 0 {
		t.Errorf("Limit = %d, want 0", cfg.Limit)
	}
	if cfg.Cursor != nil {
		t.Errorf("Cursor = %+v, want nil", cfg.Cursor)
	}
}

func TestApply_WithSort(t *testing.T) {
	t.Parallel()
	cfg := Apply(WithSort(SortUpdatedDesc))
	if cfg.Sort != SortUpdatedDesc {
		t.Errorf("Sort = %q, want %q", cfg.Sort, SortUpdatedDesc)
	}
}

func TestApply_WithSort_EmptyFallsBackToDefault(t *testing.T) {
	t.Parallel()
	cfg := Apply(WithSort(""))
	if cfg.Sort != DefaultSort {
		t.Errorf("Sort = %q, want %q (empty must coalesce to default)", cfg.Sort, DefaultSort)
	}
}

func TestApply_WithTypes_Single(t *testing.T) {
	t.Parallel()
	cfg := Apply(WithTypes("rfc"))
	if len(cfg.TypeIDs) != 1 || cfg.TypeIDs[0] != "rfc" {
		t.Errorf("TypeIDs = %v, want [rfc]", cfg.TypeIDs)
	}
}

func TestApply_WithTypes_Multiple(t *testing.T) {
	t.Parallel()
	cfg := Apply(WithTypes("rfc", "adr", "design"))
	want := []string{"rfc", "adr", "design"}
	if len(cfg.TypeIDs) != len(want) {
		t.Fatalf("TypeIDs = %v, want %v", cfg.TypeIDs, want)
	}
	for i := range want {
		if cfg.TypeIDs[i] != want[i] {
			t.Errorf("TypeIDs[%d] = %q, want %q", i, cfg.TypeIDs[i], want[i])
		}
	}
}

func TestApply_WithTypes_EmptyIsNoop(t *testing.T) {
	t.Parallel()
	cfg := Apply(WithTypes())
	if len(cfg.TypeIDs) != 0 {
		t.Errorf("WithTypes() result: TypeIDs = %v, want empty", cfg.TypeIDs)
	}
}

func TestApply_WithTypes_AppendsOnRepeat(t *testing.T) {
	t.Parallel()
	cfg := Apply(WithTypes("rfc"), WithTypes("adr"))
	if len(cfg.TypeIDs) != 2 || cfg.TypeIDs[0] != "rfc" || cfg.TypeIDs[1] != "adr" {
		t.Errorf("TypeIDs = %v, want [rfc adr] — repeated WithTypes must append", cfg.TypeIDs)
	}
}

func TestApply_WithLimit(t *testing.T) {
	t.Parallel()
	cfg := Apply(WithLimit(42))
	if cfg.Limit != 42 {
		t.Errorf("Limit = %d, want 42", cfg.Limit)
	}
}

func TestApply_WithCursor(t *testing.T) {
	t.Parallel()
	cur := &Cursor{
		Sort:      SortUpdatedDesc,
		SortValue: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ID:        domain.DocumentID("RFC-0001"),
	}
	cfg := Apply(WithCursor(cur))
	if cfg.Cursor != cur {
		t.Errorf("Cursor = %+v, want %+v (same pointer)", cfg.Cursor, cur)
	}
}

func TestApply_WithCursor_Nil(t *testing.T) {
	t.Parallel()
	cfg := Apply(WithCursor(nil))
	if cfg.Cursor != nil {
		t.Errorf("Cursor = %+v, want nil", cfg.Cursor)
	}
}

func TestApply_ComposesAllOptions(t *testing.T) {
	t.Parallel()
	cur := &Cursor{Sort: SortUpdatedDesc, ID: "RFC-0001"}
	cfg := Apply(
		WithSort(SortUpdatedDesc),
		WithTypes("rfc", "adr"),
		WithLimit(25),
		WithCursor(cur),
	)
	if cfg.Sort != SortUpdatedDesc {
		t.Errorf("Sort = %q, want %q", cfg.Sort, SortUpdatedDesc)
	}
	if len(cfg.TypeIDs) != 2 {
		t.Errorf("TypeIDs len = %d, want 2", len(cfg.TypeIDs))
	}
	if cfg.Limit != 25 {
		t.Errorf("Limit = %d, want 25", cfg.Limit)
	}
	if cfg.Cursor != cur {
		t.Errorf("Cursor mismatch")
	}
}

// TestApply_LastOptionWins pins what happens when two options set the
// same field. WithSort/WithLimit/WithCursor are last-wins (each new
// call overwrites); WithTypes is append-wins (each new call adds).
func TestApply_LastOptionWins_Sort(t *testing.T) {
	t.Parallel()
	cfg := Apply(WithSort(SortCreatedAsc), WithSort(SortUpdatedDesc))
	if cfg.Sort != SortUpdatedDesc {
		t.Errorf("Sort = %q, want %q (later WithSort overrides)", cfg.Sort, SortUpdatedDesc)
	}
}
