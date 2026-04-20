// Package middleware hosts the rfc-api HTTP middleware stack and the
// Chain helper that composes them.
//
// Middleware is the standard stdlib decorator pattern:
//
//	func(http.Handler) http.Handler
//
// Chain wraps a slice of middlewares into a single wrap, outermost
// first. Project-owned (~15 LOC) in preference to justinas/alice;
// the DIY threshold (feedback rule: < ~50 LOC) applies. See
// DESIGN-0001 Resolved Decision 10.
package middleware

import "net/http"

// Middleware wraps an http.Handler. The outer middleware runs first
// on the way in and last on the way out.
type Middleware func(http.Handler) http.Handler

// Chain composes middlewares into a single wrap. The first argument
// is the outermost wrap.
//
//	root := middleware.Chain(Otel, Recover, RequestID, Logger)
//	mux := root(http.NewServeMux())
func Chain(mws ...Middleware) Middleware {
	return func(h http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			h = mws[i](h)
		}
		return h
	}
}
