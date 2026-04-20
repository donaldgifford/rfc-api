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
	"net/url"
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
type PageInfo struct {
	Total      int
	NextCursor string
	PrevCursor string
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

func formatLink(r *http.Request, cursor, rel string) string {
	u := *r.URL
	q := u.Query()
	q.Set("cursor", cursor)
	u.RawQuery = q.Encode()
	return fmt.Sprintf("<%s>; rel=%q", urlPath(&u), rel)
}

// urlPath returns path?query for a relative Link header value. We
// intentionally omit scheme/host — the relative form matches what
// the client sent and sidesteps reverse-proxy / ingress rewriting
// (the upstream target host may not match what the client sees).
func urlPath(u *url.URL) string {
	if u.RawQuery == "" {
		return u.Path
	}
	return u.Path + "?" + u.RawQuery
}
