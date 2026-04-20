package obs_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/obs"
)

func TestNewMetrics_RegistersCollectors(t *testing.T) {
	m := obs.NewMetrics()

	// Write a couple of samples.
	m.RequestsTotal.WithLabelValues("GET", "/api/v1/rfc", "200").Inc()
	m.RequestDuration.WithLabelValues("GET", "/api/v1/rfc").Observe(0.012)
	m.InFlight.Inc()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody))

	body := rec.Body.String()
	for _, want := range []string{
		"rfc_api_http_requests_total",
		"rfc_api_http_request_duration_seconds",
		"rfc_api_http_in_flight_requests",
		"go_goroutines", // from collectors.NewGoCollector
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}
