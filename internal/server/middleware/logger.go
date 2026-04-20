package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/donaldgifford/rfc-api/internal/server/reqctx"
	"github.com/donaldgifford/rfc-api/internal/server/routectx"
)

// statusRecorder wraps http.ResponseWriter to capture the status code
// and the number of bytes written so the access log entry can report
// them. Wrapping is cheap and avoids relying on a backend-specific
// Hijacker / Flusher implementation (see func Unwrap for upgrade
// compatibility).
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err //nolint:wrapcheck // pass through underlying writer's error
}

// Unwrap returns the wrapped ResponseWriter so callers can use the
// http.ResponseController upgrade path.
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// Logger emits one slog access log per request with fields following
// OTel logs semantic conventions (flat-dotted keys). Must run inside
// RequestID so the request id is visible, and inside OTel so trace
// context is available.
//
// Emitted keys: http.request.method, url.path, http.route (when set
// by routectx -- empty on routing misses / admin endpoints),
// http.response.status_code, http.response.body.size,
// http.server.duration (ms as float), request_id, trace_id, span_id.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		durMs := float64(time.Since(start).Microseconds()) / 1000.0

		status := rec.status
		if status == 0 {
			// No explicit WriteHeader call -> net/http default is 200.
			status = http.StatusOK
		}

		attrs := []slog.Attr{
			slog.String("http.request.method", r.Method),
			slog.String("url.path", r.URL.Path),
			slog.Int("http.response.status_code", status),
			slog.Int64("http.response.body.size", rec.bytes),
			slog.Float64("http.server.duration", durMs),
			slog.String("request_id", reqctx.ID(r.Context())),
		}

		if route, ok := routectx.From(r.Context()); ok {
			attrs = append(attrs, slog.String("http.route", route.Pattern))
			if route.TypeID != "" {
				attrs = append(attrs, slog.String("rfc_api.document_type", route.TypeID))
			}
		}

		if sc := trace.SpanContextFromContext(r.Context()); sc.HasTraceID() {
			attrs = append(attrs,
				slog.String("trace_id", sc.TraceID().String()),
				slog.String("span_id", sc.SpanID().String()),
			)
		}

		slog.LogAttrs(r.Context(), slog.LevelInfo,
			r.Method+" "+r.URL.Path+" "+strconv.Itoa(status),
			attrs...,
		)
	})
}
