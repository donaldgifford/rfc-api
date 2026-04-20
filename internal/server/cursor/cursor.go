// Package cursor encodes and decodes the opaque pagination cursor the
// API exchanges with clients. The on-the-wire form is URL-safe base64
// JSON; callers outside this package should treat it as opaque.
//
// On-the-wire shape: base64url(json({"t":"<RFC3339Nano>","i":"<id>"}))
// Keeping the keys short and stable means a 64-byte cursor comfortably
// fits the 256-char header budget called out in DESIGN-0001.
package cursor

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/store"
)

// MaxLen is the hard cap on an accepted cursor header. Rejects
// oversized input before decoding to keep the parse path cheap.
const MaxLen = 256

// ErrInvalid is returned for any malformed cursor (bad base64,
// bad JSON, missing fields, unparseable time). Callers should wrap
// this with domain.ErrInvalidInput for the HTTP layer.
var ErrInvalid = errors.New("cursor: invalid")

type wire struct {
	T string `json:"t"`
	I string `json:"i"`
}

// Encode turns a store.Cursor into its base64url JSON form. A nil
// cursor encodes to the empty string (signaling "no more pages").
func Encode(c *store.Cursor) string {
	if c == nil {
		return ""
	}
	b, err := json.Marshal(wire{
		T: c.CreatedAt.UTC().Format(time.RFC3339Nano),
		I: string(c.ID),
	})
	if err != nil {
		// json.Marshal of our own struct cannot fail in practice.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode parses a base64url JSON cursor. Empty input returns (nil,
// nil) so handlers can feed r.URL.Query().Get("cursor") in directly.
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
	var w wire
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
	return &store.Cursor{CreatedAt: t, ID: domain.DocumentID(w.I)}, nil
}
