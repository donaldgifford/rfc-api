package list

import (
	"errors"
	"testing"
)

func TestParseSort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      string
		want    Sort
		wantErr error
	}{
		{"empty_returns_default", "", DefaultSort, nil},
		{"created_desc", "created_desc", SortCreatedDesc, nil},
		{"created_asc", "created_asc", SortCreatedAsc, nil},
		{"updated_desc", "updated_desc", SortUpdatedDesc, nil},
		{"updated_asc", "updated_asc", SortUpdatedAsc, nil},
		{"id_desc", "id_desc", SortIDDesc, nil},
		{"id_asc", "id_asc", SortIDAsc, nil},
		{"unknown_value", "weird", "", ErrInvalidSort},
		{"case_sensitive_rejected", "Created_Desc", "", ErrInvalidSort},
		{"whitespace_rejected", " created_desc", "", ErrInvalidSort},
		{"truncated_rejected", "created_", "", ErrInvalidSort},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSort(c.in)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("ParseSort(%q) err = %v, want wraps %v", c.in, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSort(%q) unexpected err: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("ParseSort(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestDefaultSort_PinsCreatedDesc guards DESIGN-0003 #OQ3 — flipping
// the default silently would shift every existing unfiltered caller's
// result order.
func TestDefaultSort_PinsCreatedDesc(t *testing.T) {
	t.Parallel()
	if DefaultSort != SortCreatedDesc {
		t.Errorf("DefaultSort = %q, want %q (DESIGN-0003 #OQ3 — do not change without a behavior-change release note)",
			DefaultSort, SortCreatedDesc)
	}
}

func TestSort_Valid(t *testing.T) {
	t.Parallel()

	valid := []Sort{
		SortCreatedDesc, SortCreatedAsc,
		SortUpdatedDesc, SortUpdatedAsc,
		SortIDDesc, SortIDAsc,
	}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("Sort(%q).Valid() = false, want true", s)
		}
	}

	invalid := []Sort{"", "weird", "Created_Desc", " created_desc"}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("Sort(%q).Valid() = true, want false", s)
		}
	}
}
