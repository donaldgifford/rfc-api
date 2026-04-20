package obs

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics bundles the Prometheus collectors the HTTP middleware +
// worker queue write to. A single Metrics instance is shared across
// the process; the admin server exposes it via /metrics.
//
// Registry is a fresh Registry (not the global promauto one) so test
// servers don't fight over a shared global. The admin handler
// reaches for Metrics.Gather() via the supplied promhttp.Handler.
type Metrics struct {
	Registry        *prometheus.Registry
	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec
	InFlight        prometheus.Gauge
	JobsLeased      *prometheus.CounterVec
	JobsCompleted   *prometheus.CounterVec
	JobsDead        *prometheus.CounterVec
	JobDuration     *prometheus.HistogramVec
	JobQueueDepth   *prometheus.GaugeVec
}

// labels are the three low-cardinality dimensions the HTTP middleware
// records against. Route is populated from routectx.From — never from
// the raw URL path — so cardinality is bounded by the registered
// route set.
var httpLabels = []string{"method", "route", "status"}

// durationBuckets covers the typical API latency spread: <1ms for
// local, ~10ms for DB-backed reads, up to 10s for slower paths. The
// OpenTelemetry HTTP-server histogram default doesn't translate 1:1
// to Prometheus native histograms, so we pick a fixed bucket set
// that's easy to reason about at dashboard time.
var durationBuckets = []float64{
	0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// NewMetrics constructs a Metrics with a fresh Registry and registers
// all collectors on it.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		Registry: reg,
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rfc_api_http_requests_total",
				Help: "Total HTTP requests by method, route, and status.",
			},
			httpLabels,
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "rfc_api_http_request_duration_seconds",
				Help:    "HTTP request duration, by method and route.",
				Buckets: durationBuckets,
			},
			[]string{"method", "route"},
		),
		// In-flight is unlabelled: the matched route is only known
		// after mux dispatch, and a gauge that can't be incremented
		// at request entry is useless. A single global counter is
		// sufficient for capacity-planning dashboards.
		InFlight: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rfc_api_http_in_flight_requests",
				Help: "Number of in-flight HTTP requests.",
			},
		),
	}
	m.JobsLeased = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rfc_api_worker_jobs_leased_total",
			Help: "Total jobs leased, by kind.",
		},
		[]string{"kind"},
	)
	m.JobsCompleted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rfc_api_worker_jobs_completed_total",
			Help: "Total jobs completed, by kind and result (ok|error|dead).",
		},
		[]string{"kind", "result"},
	)
	m.JobsDead = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rfc_api_worker_jobs_dead_total",
			Help: "Jobs that exhausted their retry budget and moved to dead.",
		},
		[]string{"kind"},
	)
	m.JobDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rfc_api_worker_job_duration_seconds",
			Help:    "End-to-end job handler duration, by kind.",
			Buckets: durationBuckets,
		},
		[]string{"kind"},
	)
	m.JobQueueDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rfc_api_worker_queue_depth",
			Help: "Rows in the jobs table, by kind and state.",
		},
		[]string{"kind", "state"},
	)

	reg.MustRegister(m.RequestsTotal, m.RequestDuration, m.InFlight,
		m.JobsLeased, m.JobsCompleted, m.JobsDead, m.JobDuration, m.JobQueueDepth)
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler returns a promhttp handler bound to this Metrics registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}
