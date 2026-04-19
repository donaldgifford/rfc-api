package routectx_test

import (
	"context"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/routectx"
)

func TestWithFromRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		typeID  string
		pattern string
	}{
		{"per-type route", "rfc", "/api/v1/rfc/{id}"},
		{"per-type sub", "adr", "/api/v1/adr/{id}/links"},
		{"cross-type", "", "/api/v1/docs"},
		{"admin", "", "/healthz"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := routectx.With(context.Background(), tc.typeID, tc.pattern)
			got, ok := routectx.From(ctx)
			if !ok {
				t.Fatal("From() ok = false, want true")
			}
			if got.TypeID != tc.typeID {
				t.Errorf("TypeID = %q, want %q", got.TypeID, tc.typeID)
			}
			if got.Pattern != tc.pattern {
				t.Errorf("Pattern = %q, want %q", got.Pattern, tc.pattern)
			}
		})
	}
}

func TestFromMissing(t *testing.T) {
	t.Parallel()

	_, ok := routectx.From(context.Background())
	if ok {
		t.Fatal("From(bare ctx) ok = true, want false")
	}
}
