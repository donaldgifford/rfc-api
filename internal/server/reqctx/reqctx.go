// Package reqctx carries the per-request correlation id on request
// context.
//
// The request-id middleware writes it (from the X-Request-ID header,
// the active OTel trace id, or crypto/rand). Handlers, the logger,
// and the RFC 7807 error writer read it. Keeping the key in its own
// package prevents an import cycle between middleware and httperr.
package reqctx

import "context"

type ctxKey struct{}

// WithID returns ctx decorated with the given request id.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// ID returns the request id on ctx, or "" if none was set.
func ID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}
