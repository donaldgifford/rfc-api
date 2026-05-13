// Package cursor encodes and decodes the opaque pagination cursor the
// API exchanges with clients. The on-the-wire form is URL-safe base64
// JSON; callers outside this package should treat it as opaque.
//
// v1 wire shape (IMPL-0007):
//
//	base64url(json({"v":1,"s":"<sort>","k":["<value>","<id>"]}))
//
// Legacy wire shape (pre-IMPL-0007):
//
//	base64url(json({"t":"<RFC3339Nano>","i":"<id>"}))
//
// Decode accepts either; legacy tokens decode as list.SortCreatedDesc
// so cursors minted before IMPL-0007 stay honored. Encode always emits
// v1 going forward.
//
// Keeping the keys short (`v`, `s`, `k`) means a typical cursor still
// fits comfortably inside the 256-char header budget called out in
// DESIGN-0001.
package cursor

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store"
	"github.com/donaldgifford/rfc-api/internal/store/list"
)

// MaxLen is the hard cap on an accepted cursor header. Rejects
// oversized input before decoding to keep the parse path cheap.
const MaxLen = 256

// ErrInvalid is returned for any malformed cursor (bad base64,
// bad JSON, missing fields, unparseable time). Callers should wrap
// this with domain.ErrInvalidInput for the HTTP layer.
var ErrInvalid = errors.New("cursor: invalid")

// wireV1 is the IMPL-0007 envelope: V is the schema version, S the
// active sort, K[0] the sort-column value (RFC3339Nano for time-based
// sorts, empty for id-based sorts), K[1] the tiebreaker id.
type wireV1 struct {
	V int       `json:"v"`
	S list.Sort `json:"s"`
	K [2]string `json:"k"`
}

// wireLegacy is the pre-IMPL-0007 envelope. Decode falls back to this
// shape when the new fields are absent.
type wireLegacy struct {
	T string `json:"t"`
	I string `json:"i"`
}

// Encode turns a store.Cursor into its base64url JSON form. A nil
// cursor encodes to the empty string (signaling "no more pages").
//
// Encode always emits the v1 envelope. A cursor with a zero Sort
// is treated as list.SortCreatedDesc, matching the legacy-decode
// fallback semantics.
func Encode(c *store.Cursor) string {
	if c == nil {
		return ""
	}
	sort := c.Sort
	if sort == "" {
		sort = list.DefaultSort
	}
	var sortValue string
	if isTimeSort(sort) {
		sortValue = c.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(wireV1{
		V: 1,
		S: sort,
		K: [2]string{sortValue, string(c.ID)},
	})
	if err != nil {
		// json.Marshal of our own struct cannot fail in practice.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode parses a base64url JSON cursor. Empty input returns (nil,
// nil) so handlers can feed r.URL.Query().Get("cursor") in directly.
//
// Decode accepts both the v1 envelope and the legacy {t,i} shape.
// Legacy cursors return store.Cursor{Sort: list.SortCreatedDesc, …}
// so the rest of the pipeline can treat the two uniformly.
func Decode(s string) (*store.Cursor, error) {
	if s == "" {
		return nil, nil //nolint:nilnil // empty cursor is a valid "no cursor" input
	}
	if len(s) > MaxLen {
		return nil, fmt.Errorf("%w: too long", ErrInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: base64: %w", ErrInvalid, err)
	}

	// Sniff which envelope this is. A v1 cursor always has "v":1.
	var probe struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("%w: json: %w", ErrInvalid, err)
	}
	if probe.V == 1 {
		return decodeV1(raw)
	}
	return decodeLegacy(raw)
}

func decodeV1(raw []byte) (*store.Cursor, error) {
	var w wireV1
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("%w: json: %w", ErrInvalid, err)
	}
	if !w.S.Valid() {
		return nil, fmt.Errorf("%w: unknown sort %q", ErrInvalid, w.S)
	}
	id := w.K[1]
	if id == "" {
		return nil, fmt.Errorf("%w: missing id", ErrInvalid)
	}
	cur := &store.Cursor{Sort: w.S, ID: domain.DocumentID(id)}
	if isTimeSort(w.S) {
		if w.K[0] == "" {
			return nil, fmt.Errorf("%w: missing sort value for %s", ErrInvalid, w.S)
		}
		t, err := time.Parse(time.RFC3339Nano, w.K[0])
		if err != nil {
			return nil, fmt.Errorf("%w: time: %w", ErrInvalid, err)
		}
		cur.CreatedAt = t
	}
	return cur, nil
}

func decodeLegacy(raw []byte) (*store.Cursor, error) {
	var w wireLegacy
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("%w: json: %w", ErrInvalid, err)
	}
	if w.T == "" || w.I == "" {
		return nil, fmt.Errorf("%w: missing fields", ErrInvalid)
	}
	t, err := time.Parse(time.RFC3339Nano, w.T)
	if err != nil {
		return nil, fmt.Errorf("%w: time: %w", ErrInvalid, err)
	}
	return &store.Cursor{
		Sort:      list.SortCreatedDesc,
		CreatedAt: t,
		ID:        domain.DocumentID(w.I),
	}, nil
}

// isTimeSort reports whether the active sort keys on a time column
// (created_at or updated_at). For these sorts the cursor's K[0] holds
// an RFC3339Nano timestamp; for id-based sorts K[0] is empty.
func isTimeSort(s list.Sort) bool {
	switch s {
	case list.SortCreatedDesc, list.SortCreatedAsc,
		list.SortUpdatedDesc, list.SortUpdatedAsc:
		return true
	}
	return false
}
