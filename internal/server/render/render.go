// Package render centralizes HTTP response formatting for the API.
// Handlers call JSON / ArrayJSON for success responses and
// internal/server/httperr.Write for errors. Keeping serialization in
// one place means the Content-Type, pagination headers, and envelope
// shape can only change in one spot.
package render

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

// contentType is the success-response media type. Errors use
// application/problem+json via internal/server/httperr.
const contentType = "application/json"

// PageInfo carries the pagination metadata for a list response. Used
// by ArrayJSON to set X-Total-Count and Link headers per DESIGN-0001
// #API surface. NextCursor empty = last page; PrevCursor empty when
// no previous page is known (e.g. first request).
//
// TotalUnfiltered is the IMPL-0007 #X-Total-Count-Unfiltered seam:
// when non-nil, ArrayJSON sets the header to its value. Handler
// passes nil for unfiltered requests so the header is omitted, and
// a *int for filtered requests so the client can distinguish "no
// matches" (filtered total=0) from "no documents at all"
// (unfiltered total=0). Pointer (not zero int) so the omission is
// explicit at the call site.
type PageInfo struct {
	Total           int
	NextCursor      string
	PrevCursor      string
	TotalUnfiltered *int
}

// JSON writes a success response with status and v as the body.
// v is typically a domain struct tagged for JSON output.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Default().Error("render.JSON encode", "err", err.Error())
	}
}

// ArrayJSON writes a list-endpoint response. The body is a bare JSON
// array (per DESIGN-0001 Resolved Decision 5: "list endpoints return
// bare arrays, pagination in headers") with X-Total-Count and Link
// headers populated from info.
//
// r is required so the Link header's URLs mirror the caller's
// request path — callers following next/prev hit the same endpoint
// with the right query-string overrides.
//
// A nil items slice is treated as an empty array so the response
// body is always JSON `[]` (never `null`). The OpenAPI contract
// test is strict about this and real clients get a cleaner shape.
func ArrayJSON(w http.ResponseWriter, r *http.Request, items any, info PageInfo) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Total-Count", strconv.Itoa(info.Total))
	if info.TotalUnfiltered != nil {
		h.Set("X-Total-Count-Unfiltered", strconv.Itoa(*info.TotalUnfiltered))
	}
	if link := buildLinkHeader(r, info); link != "" {
		h.Set("Link", link)
	}
	w.WriteHeader(http.StatusOK)
	payload := items
	if isNilSlice(items) {
		payload = []struct{}{}
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Default().Error("render.ArrayJSON encode", "err", err.Error())
	}
}

// isNilSlice reports whether v is a typed-nil slice. Needed because
// (any)(nil-typed-slice) != nil but marshals to JSON null.
func isNilSlice(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice && rv.IsNil()
}

// buildLinkHeader assembles the RFC 8288 Link header with rel="next"
// and rel="prev" values when corresponding cursors are set.
func buildLinkHeader(r *http.Request, info PageInfo) string {
	parts := make([]string, 0, 2)
	if info.NextCursor != "" {
		parts = append(parts, formatLink(r, info.NextCursor, "next"))
	}
	if info.PrevCursor != "" {
		parts = append(parts, formatLink(r, info.PrevCursor, "prev"))
	}
	return strings.Join(parts, ", ")
}

// formatLink builds one `<url>; rel="<rel>"` entry. Path resolution
// prefers r.RequestURI over r.URL.Path so the Link survives any
// http.StripPrefix the router applied (the API mounts the v1 mux
// under `/api/v1/` via StripPrefix, which rewrites r.URL.Path but
// leaves RequestURI untouched). Without this, the Link header
// emitted for `/api/v1/docs?...` would point at `/docs?...` and
// clients following it would 404 against the unstripped mux.
func formatLink(r *http.Request, cursor, rel string) string {
	base, ok := requestURIPath(r)
	if !ok {
		base = r.URL.Path
	}
	q := r.URL.Query()
	q.Set("cursor", cursor)
	out := base
	if encoded := q.Encode(); encoded != "" {
		out = base + "?" + encoded
	}
	return fmt.Sprintf("<%s>; rel=%q", out, rel)
}

// requestURIPath splits r.RequestURI into its path component. Falls
// back to (empty, false) for malformed input so callers can defer
// to r.URL.Path; RequestURI may be empty (e.g. when a handler is
// invoked outside an HTTP server context).
func requestURIPath(r *http.Request) (string, bool) {
	if r.RequestURI == "" {
		return "", false
	}
	if i := strings.IndexByte(r.RequestURI, '?'); i >= 0 {
		return r.RequestURI[:i], true
	}
	return r.RequestURI, true
}
