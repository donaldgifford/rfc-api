// Package docid holds the pure conversion helpers between the two
// document-id forms defined in DESIGN-0002 §Identifier format:
//
//   - URL id: numeric only, zero-padded — e.g. "0001"
//   - Canonical display id: uppercase prefix + "-" + URL id — e.g.
//     "RFC-0001"
//
// These helpers do no I/O. In particular the read hot path does not
// call the registry: handlers take (typeID, urlID) from the route and
// produce a canonical id via docid.Canonical. Prefix uniqueness is a
// load-time invariant owned by the registry; once the registry has
// loaded, lowercase(prefix) == typeID for every registered type.
package docid

import (
	"strings"

	"github.com/donaldgifford/rfc-api/internal/domain"
)

// Canonical builds the canonical display id for a (typeID, urlID)
// pair. typeID is the route segment ("rfc"); urlID is the numeric
// segment ("0001"). No input validation beyond uppercasing the prefix
// — callers should validate urlID shape separately via ParseURLID or
// the router.
func Canonical(typeID, urlID string) domain.DocumentID {
	return domain.DocumentID(strings.ToUpper(typeID) + "-" + urlID)
}

// Parse splits a canonical display id into its (typeID, urlID) pair.
// typeID is lowercased so it matches route segments directly. Returns
// ok=false when the input does not contain exactly one "-" or when
// either side is empty.
func Parse(displayID domain.DocumentID) (typeID, urlID string, ok bool) {
	s := string(displayID)
	idx := strings.IndexByte(s, '-')
	if idx <= 0 || idx == len(s)-1 {
		return "", "", false
	}
	// A second dash rejects malformed input like "RFC-00-01".
	if strings.IndexByte(s[idx+1:], '-') >= 0 {
		return "", "", false
	}
	return strings.ToLower(s[:idx]), s[idx+1:], true
}

// URLForm extracts the URL-path numeric id from a canonical display
// id. Returns the empty string for malformed input.
func URLForm(displayID domain.DocumentID) string {
	_, urlID, ok := Parse(displayID)
	if !ok {
		return ""
	}
	return urlID
}
