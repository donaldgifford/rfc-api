package cursor_test

import (
	"errors"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/server/cursor"
	"github.com/donaldgifford/rfc-api/internal/store"
)

func TestRoundTrip(t *testing.T) {
	want := &store.Cursor{CreatedAt: time.Date(2026, 4, 19, 15, 0, 0, 0, time.UTC), ID: "RFC-0001"}
	s := cursor.Encode(want)
	if s == "" {
		t.Fatal("encode returned empty")
	}
	got, err := cursor.Decode(s)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID {
		t.Errorf("round-trip: got %+v, want %+v", got, want)
	}
}

func TestEncode_Nil(t *testing.T) {
	if got := cursor.Encode(nil); got != "" {
		t.Errorf("Encode(nil) = %q, want empty", got)
	}
}

func TestDecode_Empty(t *testing.T) {
	got, err := cursor.Decode("")
	if err != nil {
		t.Fatalf("Decode(empty) err = %v", err)
	}
	if got != nil {
		t.Errorf("Decode(empty) = %+v, want nil", got)
	}
}

func TestDecode_Bad(t *testing.T) {
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
	long := make([]byte, cursor.MaxLen+1)
	for i := range long {
		long[i] = 'a'
	}
	_, err := cursor.Decode(string(long))
	if !errors.Is(err, cursor.ErrInvalid) {
		t.Errorf("Decode(oversize) err = %v, want ErrInvalid", err)
	}
}
