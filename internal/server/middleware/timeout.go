package middleware

import (
	"context"
	"net/http"
	"time"
)

// Timeout wraps every request in context.WithTimeout(r.Context(), d).
// Downstream handlers that respect ctx (services, store calls) will
// return early with ctx.Err() when the budget is exhausted; the
// response itself is still the handler's responsibility. This is the
// standard pattern — wrapping via http.TimeoutHandler inserts a
// whole-response-bytes timeout that interacts badly with streaming
// handlers and with our own RFC 7807 writer, so we stay with context
// cancellation and let each handler decide how to surface the error.
//
// d ≤ 0 disables the middleware — useful for admin port construction
// where long-running pprof profiles must not be killed.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
