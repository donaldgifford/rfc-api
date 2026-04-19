package middleware

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/donaldgifford/rfc-api/internal/server/httperr"
)

// errPanicRecovered wraps a recovered panic as an error value so the
// httperr writer can log it with stack + request id without special-
// casing panics.
var errPanicRecovered = errors.New("panic recovered")

// Recover catches panics below it in the chain, logs the stack under
// the active slog default logger, and writes a 500 RFC 7807 response
// via httperr. The process does NOT crash.
//
// Outer only; place right after OTel so traces / log correlation still
// capture the panicking request.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}

			// Log with stack server-side so operators get the full
			// picture; the client gets the fixed 500 detail.
			slog.ErrorContext(r.Context(), "panic in handler",
				"panic", fmt.Sprintf("%v", rec),
				"stack", string(debug.Stack()),
			)

			// Best-effort 500 response. If the handler already wrote
			// headers (rare) this may be a no-op -- that's fine.
			httperr.Write(w, r, fmt.Errorf("%w: %v", errPanicRecovered, rec))
		}()

		next.ServeHTTP(w, r)
	})
}
