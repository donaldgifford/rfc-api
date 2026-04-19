// Package domain holds framework-agnostic business types and sentinel
// errors used across services, stores, and HTTP handlers.
//
// domain never imports net/http, database/sql, or any HTTP framework.
// The HTTP seam (internal/server) translates these errors into RFC 7807
// problem+json responses via internal/server/httperr.
package domain

import "errors"

// Sentinel domain errors. Every service call, every store call, and
// every search call returns one of these (via wrap chains) when the
// failure mode is one callers need to branch on.
//
// Unknown failures (returning anything that doesn't match these)
// surface as 500 at the HTTP layer; see DESIGN-0001 §Error handling.
var (
	// ErrNotFound is returned when a requested resource does not exist.
	// Maps to HTTP 404.
	ErrNotFound = errors.New("not found")

	// ErrInvalidInput is returned for malformed or otherwise unusable
	// client input (bad cursor, out-of-range limit, unknown type id).
	// Maps to HTTP 400.
	ErrInvalidInput = errors.New("invalid input")

	// ErrConflict is returned when a request races a concurrent state
	// change or violates a uniqueness constraint. Maps to HTTP 409.
	ErrConflict = errors.New("conflict")

	// ErrUpstream is returned when a downstream dep (database,
	// Meilisearch, GitHub, OIDC provider) is the cause of the failure.
	// Maps to HTTP 502.
	ErrUpstream = errors.New("upstream failure")
)
