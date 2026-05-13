package handler

import (
	"errors"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/store/list"
)

func TestParseFilters(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		in        []string
		want      []filter
		wantErr   bool
		errSubstr string
	}{
		{
			name: "empty_input_returns_nil",
			in:   nil,
			want: nil,
		},
		{
			name: "single_type_filter",
			in:   []string{"type:rfc"},
			want: []filter{{Field: "type", Value: "rfc"}},
		},
		{
			name: "multiple_same_field",
			in:   []string{"type:rfc", "type:adr"},
			want: []filter{
				{Field: "type", Value: "rfc"},
				{Field: "type", Value: "adr"},
			},
		},
		{
			name: "multiple_distinct_fields",
			in:   []string{"type:rfc", "status:accepted"},
			want: []filter{
				{Field: "type", Value: "rfc"},
				{Field: "status", Value: "accepted"},
			},
		},
		{
			name: "value_can_contain_hyphen",
			in:   []string{"type:design-system"},
			want: []filter{{Field: "type", Value: "design-system"}},
		},
		{
			name: "value_can_contain_underscore",
			in:   []string{"type:my_type"},
			want: []filter{{Field: "type", Value: "my_type"}},
		},

		// --- malformed shape ---
		{name: "missing_colon", in: []string{"typerfc"}, wantErr: true, errSubstr: "missing ':'"},
		{name: "multiple_colons", in: []string{"type:rfc:extra"}, wantErr: true, errSubstr: "missing ':'"},
		{name: "empty_field", in: []string{":rfc"}, wantErr: true, errSubstr: "empty field"},
		{name: "empty_value", in: []string{"type:"}, wantErr: true, errSubstr: "empty value"},
		{name: "blank_string", in: []string{""}, wantErr: true},

		// --- invalid field shape ---
		{name: "field_starts_with_digit", in: []string{"1type:rfc"}, wantErr: true, errSubstr: "field"},
		{name: "field_starts_with_underscore", in: []string{"_type:rfc"}, wantErr: true, errSubstr: "field"},
		{name: "field_contains_hyphen", in: []string{"my-field:rfc"}, wantErr: true, errSubstr: "field"},
		{name: "field_contains_uppercase", in: []string{"Type:rfc"}, wantErr: true, errSubstr: "field"},

		// --- invalid value shape ---
		{name: "value_contains_space", in: []string{"type:r fc"}, wantErr: true, errSubstr: "value"},
		{name: "value_contains_dot", in: []string{"version:1.2.3"}, wantErr: true, errSubstr: "value"},
		{name: "value_contains_slash", in: []string{"path:foo/bar"}, wantErr: true, errSubstr: "value"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseFilters(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseFilters(%v) = %v, %v, want error", c.in, got, err)
				}
				if !errors.Is(err, errBadFilter) {
					t.Errorf("parseFilters err = %v, want wraps errBadFilter", err)
				}
				if c.errSubstr != "" && !containsString(err.Error(), c.errSubstr) {
					t.Errorf("parseFilters err = %q, want substring %q", err.Error(), c.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFilters(%v) unexpected err: %v", c.in, err)
			}
			if !equalFilters(got, c.want) {
				t.Errorf("parseFilters(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestParseFilters_ErrorNamesRowIndex pins the contract that
// parseFilters' error message includes the index of the offending
// row so callers diffing 400 responses can see which filter[] entry
// failed. Important when rfc-site's loader concatenates 5+ filter
// values and one of them is malformed.
func TestParseFilters_ErrorNamesRowIndex(t *testing.T) {
	t.Parallel()
	_, err := parseFilters([]string{"type:rfc", "bad", "type:adr"})
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	if !containsString(err.Error(), "index 1") {
		t.Errorf("err = %q, want substring %q", err.Error(), "index 1")
	}
}

func TestParseSort(t *testing.T) {
	t.Parallel()

	t.Run("empty_returns_default", func(t *testing.T) {
		t.Parallel()
		got, err := parseSort("")
		if err != nil {
			t.Fatalf("parseSort empty: %v", err)
		}
		if got != list.DefaultSort {
			t.Errorf("parseSort(\"\") = %q, want %q", got, list.DefaultSort)
		}
	})

	t.Run("valid_value_passes_through", func(t *testing.T) {
		t.Parallel()
		got, err := parseSort("updated_desc")
		if err != nil {
			t.Fatalf("parseSort updated_desc: %v", err)
		}
		if got != list.SortUpdatedDesc {
			t.Errorf("parseSort(updated_desc) = %q, want %q", got, list.SortUpdatedDesc)
		}
	})

	t.Run("invalid_value_wraps_errBadSort_and_listSort_sentinel", func(t *testing.T) {
		t.Parallel()
		_, err := parseSort("weird")
		if err == nil {
			t.Fatalf("want error, got nil")
		}
		if !errors.Is(err, errBadSort) {
			t.Errorf("err = %v, want wraps errBadSort", err)
		}
		if !errors.Is(err, list.ErrInvalidSort) {
			t.Errorf("err = %v, want chain to include list.ErrInvalidSort", err)
		}
	})
}

// containsString is a small substring check used so we can match
// substrings inside formatted error messages without committing to
// the exact wording.
func containsString(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func equalFilters(a, b []filter) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
