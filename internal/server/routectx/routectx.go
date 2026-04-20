// Package routectx carries the matched-route metadata (DocumentType id
// and pattern template) on request context.
//
// The closure at route registration stashes these via With; handlers,
// the logger, the metrics middleware, and the OTel span namer all
// read them via From. This is the single mechanism for route metadata
// in rfc-api -- no code reads r.Pattern directly (per DESIGN-0001
// Resolved Decision 9). One grep scope if we ever swap context for
// another propagation mechanism.
package routectx

import "context"

// Route is the matched-route metadata stored on request context.
//
// TypeID is the lowercase DocumentType id ("rfc", "adr", ...) for
// per-type routes, or the empty string for cross-type routes
// (/api/v1/docs, /api/v1/search, /api/v1/types) and admin-port
// routes.
//
// Pattern is the full matched route template as it appears in the
// mux registration, e.g. "/api/v1/rfc/{id}" or "/healthz". Used
// as-is for metric labels, span names, and access-log fields.
type Route struct {
	TypeID  string
	Pattern string
}

// ctxKey and captureKey are unexported types so callers can't forge
// routectx keys.
type (
	ctxKey     struct{}
	captureKey struct{}
)

// Capture is a mutable route slot installed before the mux has
// dispatched. Middleware that needs the matched pattern *after*
// ServeHTTP returns (metrics, access-log — anything in the outer
// wrap chain that can't read a derived context) installs a Capture
// via WithCapture, then reads it back post-dispatch. The withRoute
// closure populates the capture in addition to setting a derived
// context, so both pre- and post-handler readers see the same value.
type Capture struct {
	Route Route
}

// With returns a derived context carrying the given route metadata.
// If ctx has a Capture installed by WithCapture, With also writes
// the route into the capture so outer middleware can read it
// post-dispatch.
func With(ctx context.Context, typeID, pattern string) context.Context {
	route := Route{TypeID: typeID, Pattern: pattern}
	if capture, ok := ctx.Value(captureKey{}).(*Capture); ok && capture != nil {
		capture.Route = route
	}
	return context.WithValue(ctx, ctxKey{}, route)
}

// WithCapture installs a fresh *Capture on ctx. Returns the new
// context and the capture pointer the caller should read after
// ServeHTTP returns.
func WithCapture(ctx context.Context) (context.Context, *Capture) {
	c := &Capture{}
	return context.WithValue(ctx, captureKey{}, c), c
}

// From returns the Route stashed on ctx, and a bool that's false when
// no route metadata has been attached (routing miss, unwrapped
// handler, etc.).
func From(ctx context.Context) (Route, bool) {
	r, ok := ctx.Value(ctxKey{}).(Route)
	return r, ok
}
