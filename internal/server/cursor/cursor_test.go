package cursor_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/server/cursor"
	"github.com/donaldgifford/rfc-api/internal/store/list"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	want := &list.Cursor{
		Sort:      list.SortCreatedDesc,
		SortValue: time.Date(2026, 4, 19, 15, 0, 0, 0, time.UTC),
		ID:        "RFC-0001",
	}
	s := cursor.Encode(want)
	if s == "" {
		t.Fatal("encode returned empty")
	}
	got, err := cursor.Decode(s)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Sort != want.Sort || !got.SortValue.Equal(want.SortValue) || got.ID != want.ID {
		t.Errorf("round-trip: got %+v, want %+v", got, want)
	}
}

// TestRoundTrip_EverySort proves the v1 envelope decodes back to the
// same Sort + key for every documented enum value, including the
// id-only sorts whose K[0] timestamp slot is empty.
func TestRoundTrip_EverySort(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 19, 15, 0, 0, 0, time.UTC)
	id := "RFC-0001"

	cases := []list.Sort{
		list.SortCreatedDesc, list.SortCreatedAsc,
		list.SortUpdatedDesc, list.SortUpdatedAsc,
		list.SortIDDesc, list.SortIDAsc,
	}
	for _, s := range cases {
		t.Run(string(s), func(t *testing.T) {
			t.Parallel()
			in := &list.Cursor{Sort: s, SortValue: ts, ID: domain.DocumentID(id)}
			enc := cursor.Encode(in)
			if enc == "" {
				t.Fatalf("Encode returned empty for sort %s", s)
			}
			out, err := cursor.Decode(enc)
			if err != nil {
				t.Fatalf("Decode(%s): %v", s, err)
			}
			if out.Sort != s {
				t.Errorf("Decode(%s).Sort = %q, want %q", s, out.Sort, s)
			}
			if out.ID != domain.DocumentID(id) {
				t.Errorf("Decode(%s).ID = %q, want %q", s, out.ID, id)
			}
			// Time-based sorts carry their timestamp; id-based sorts
			// have a zero CreatedAt and the store dispatches on ID alone.
			if isTimeSort(s) && !out.SortValue.Equal(ts) {
				t.Errorf("Decode(%s).SortValue = %v, want %v", s, out.SortValue, ts)
			}
			if !isTimeSort(s) && !out.SortValue.IsZero() {
				t.Errorf("Decode(%s).SortValue = %v, want zero (id sort)", s, out.SortValue)
			}
		})
	}
}

// TestDecode_LegacyEnvelope_AssumesCreatedDesc pins that pre-IMPL-0007
// cursors (no v/s/k fields) still decode cleanly and surface as a
// list.SortCreatedDesc cursor. Without this the rfc-site reload after
// the bump would 400 every cursor it had cached.
func TestDecode_LegacyEnvelope_AssumesCreatedDesc(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 19, 15, 0, 0, 0, time.UTC)
	id := "RFC-0001"

	legacyJSON, err := json.Marshal(map[string]string{
		"t": ts.Format(time.RFC3339Nano),
		"i": id,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	legacy := base64.RawURLEncoding.EncodeToString(legacyJSON)

	got, err := cursor.Decode(legacy)
	if err != nil {
		t.Fatalf("Decode(legacy): %v", err)
	}
	if got.Sort != list.SortCreatedDesc {
		t.Errorf("legacy.Sort = %q, want %q", got.Sort, list.SortCreatedDesc)
	}
	if !got.SortValue.Equal(ts) {
		t.Errorf("legacy.SortValue = %v, want %v", got.SortValue, ts)
	}
	if got.ID != domain.DocumentID(id) {
		t.Errorf("legacy.ID = %q, want %q", got.ID, id)
	}
}

// TestEncode_ZeroSortDefaultsToCreatedDesc covers the migration path:
// callers (like today's in-memory store) that build a *list.Cursor
// without setting Sort still produce a valid, decodeable cursor.
func TestEncode_ZeroSortDefaultsToCreatedDesc(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 19, 15, 0, 0, 0, time.UTC)
	zeroSort := &list.Cursor{SortValue: ts, ID: "RFC-0001"} // Sort intentionally zero

	enc := cursor.Encode(zeroSort)
	got, err := cursor.Decode(enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Sort != list.SortCreatedDesc {
		t.Errorf("Sort = %q, want %q", got.Sort, list.SortCreatedDesc)
	}
}

// TestDecode_UnknownSort_Rejected pins that a v1 envelope with a
// sort value outside the documented enum returns ErrInvalid. Keeps
// an attacker (or a buggy older client) from confusing the store
// with a sort key it doesn't dispatch on.
func TestDecode_UnknownSort_Rejected(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(map[string]any{
		"v": 1,
		"s": "weird_sort",
		"k": []string{"2026-04-19T15:00:00Z", "RFC-0001"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	tok := base64.RawURLEncoding.EncodeToString(payload)

	_, err = cursor.Decode(tok)
	if !errors.Is(err, cursor.ErrInvalid) {
		t.Errorf("Decode err = %v, want ErrInvalid", err)
	}
}

// TestDecode_IDSort_RejectsTimestampPresence is not the inverse —
// id sorts with K[0] populated are tolerated (parsed but ignored).
// What we DO reject is a time sort with K[0] empty.
func TestDecode_TimeSort_RequiresTimestamp(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(map[string]any{
		"v": 1,
		"s": "updated_desc",
		"k": []string{"", "RFC-0001"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	tok := base64.RawURLEncoding.EncodeToString(payload)

	_, err = cursor.Decode(tok)
	if !errors.Is(err, cursor.ErrInvalid) {
		t.Errorf("Decode err = %v, want ErrInvalid", err)
	}
}

func TestEncode_Nil(t *testing.T) {
	t.Parallel()
	if got := cursor.Encode(nil); got != "" {
		t.Errorf("Encode(nil) = %q, want empty", got)
	}
}

func TestDecode_Empty(t *testing.T) {
	t.Parallel()
	got, err := cursor.Decode("")
	if err != nil {
		t.Fatalf("Decode(empty) err = %v", err)
	}
	if got != nil {
		t.Errorf("Decode(empty) = %+v, want nil", got)
	}
}

func TestDecode_Bad(t *testing.T) {
	t.Parallel()
	cases := []string{
		"!!!not-base64!!!",
		"dGhpc2lzbm90anNvbg", // base64 of "thisisnotjson"
	}
	for _, s := range cases {
		_, err := cursor.Decode(s)
		if !errors.Is(err, cursor.ErrInvalid) {
			t.Errorf("Decode(%q) err = %v, want ErrInvalid", s, err)
		}
	}
}

func TestDecode_TooLong(t *testing.T) {
	t.Parallel()
	long := make([]byte, cursor.MaxLen+1)
	for i := range long {
		long[i] = 'a'
	}
	_, err := cursor.Decode(string(long))
	if !errors.Is(err, cursor.ErrInvalid) {
		t.Errorf("Decode(oversize) err = %v, want ErrInvalid", err)
	}
}

// isTimeSort mirrors the cursor-package-internal helper. Duplicated
// here so the test stays decoupled from the implementation; if the
// package definition shifts, the test catches it via the v1 round-trip.
func isTimeSort(s list.Sort) bool {
	switch s {
	case list.SortCreatedDesc, list.SortCreatedAsc,
		list.SortUpdatedDesc, list.SortUpdatedAsc:
		return true
	}
	return false
}
