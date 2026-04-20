package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"go.opentelemetry.io/otel/trace"

	"github.com/donaldgifford/rfc-api/internal/server/reqctx"
)

// HeaderRequestID is the canonical HTTP header name for the rfc-api
// correlation id. DESIGN-0001 Resolved Decision 9.
const HeaderRequestID = "X-Request-ID"

// RequestID stashes a correlation id on request context and echoes it
// in the response header.
//
// Source precedence:
//  1. X-Request-ID header supplied by the client (reused as-is).
//  2. Active OTel trace id from ctx, when a span is already in flight
//     (i.e. otelhttp ran outer). Keeps request-id and trace-id the
//     same string so logs / metrics / traces cross-reference trivially.
//  3. 16 bytes from crypto/rand hex-encoded.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := resolveRequestID(r)

		ctx := reqctx.WithID(r.Context(), id)
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func resolveRequestID(r *http.Request) string {
	if v := r.Header.Get(HeaderRequestID); v != "" {
		return v
	}
	if sc := trace.SpanContextFromContext(r.Context()); sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return randomID()
}

func randomID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failures on modern OSes indicate broken entropy
		// sources -- unrecoverable, fall back to a clearly-visible
		// marker so the failure shows up in logs / dashboards rather
		// than silently producing duplicate ids.
		return "rand-failed-" + hex.EncodeToString(buf[:8])
	}
	return hex.EncodeToString(buf[:])
}
