package worker

import (
	"time"

	"github.com/donaldgifford/rfc-api/internal/obs"
	"github.com/donaldgifford/rfc-api/internal/worker/queue"
)

// metricsAdapter bridges obs.Metrics to queue.LeaseMetrics so the
// queue package stays framework-agnostic.
type metricsAdapter struct{ m *obs.Metrics }

func newLeaseMetrics(m *obs.Metrics) queue.LeaseMetrics {
	if m == nil {
		return queue.NoopMetrics{}
	}
	return metricsAdapter{m: m}
}

func (a metricsAdapter) JobLeased(kind string) {
	a.m.JobsLeased.WithLabelValues(kind).Inc()
}

func (a metricsAdapter) JobCompleted(kind, result string, duration time.Duration) {
	a.m.JobsCompleted.WithLabelValues(kind, result).Inc()
	a.m.JobDuration.WithLabelValues(kind).Observe(duration.Seconds())
}

func (a metricsAdapter) JobDead(kind string) {
	a.m.JobsDead.WithLabelValues(kind).Inc()
}
