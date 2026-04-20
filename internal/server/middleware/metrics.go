package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/donaldgifford/rfc-api/internal/obs"
	"github.com/donaldgifford/rfc-api/internal/server/routectx"
)

// Metrics records Prometheus counters and histograms for every
// request. Route label comes from routectx — populated by the
// router's withRoute closure — so cardinality is bounded by the
// registered route set. Requests to the catch-all 404 land under
// route="" which keeps the label set small; operators can filter it
// out of dashboards if they want.
//
// In-flight gauge is incremented before the handler runs and
// decremented in a defer so panics don't leave it skewed.
func Metrics(m *obs.Metrics) Middleware {
	if m == nil {
		return passThrough
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Install a Capture before dispatch: withRoute writes the
			// matched pattern into it, and we read it back after
			// ServeHTTP to populate metric labels. Using a capture
			// (not r.Pattern) keeps routectx the single source of
			// truth for route metadata.
			ctx, capture := routectx.WithCapture(r.Context())
			r = r.WithContext(ctx)

			m.InFlight.Inc()
			defer m.InFlight.Dec()

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(rec, r)
			elapsed := time.Since(start).Seconds()

			route := capture.Route.Pattern
			m.RequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
			m.RequestDuration.WithLabelValues(r.Method, route).Observe(elapsed)
		})
	}
}
