package middleware

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

// OTel returns a Middleware that wraps the inner handler with
// otelhttp instrumentation. Outermost on both the main and admin
// chains per DESIGN-0001 §Middleware chain.
//
// Span names at creation time are a placeholder ("rfc-api:<method>");
// the per-route closure (Phase 2 withRoute) renames the span to
// "METHOD <pattern>" once the mux has dispatched and routectx is
// populated. This is necessary because otelhttp wraps the Handler
// outside the mux -- r.Pattern / routectx aren't populated when the
// span is first created.
func OTel(tp trace.TracerProvider) Middleware {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(
			next,
			"rfc-api",
			otelhttp.WithTracerProvider(tp),
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return "rfc-api:" + r.Method
			}),
		)
	}
}
