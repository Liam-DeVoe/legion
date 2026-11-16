package natscallout

import (
	"time"

	"github.com/sjawhar/envoy/internal/metrics"
)

// Metrics counts the service's decisions: broker_nats_callout_decisions_total by credential kind,
// outcome and reason, which is the production census of the services the hub admits (its
// kind="service" series); broker_nats_callout_seconds, how long each decision took from the
// request's arrival; and broker_nats_callout_shared_lanes_dropped_total, the repositories withheld
// from a held daemon's grant because another project maps their lane (scopeGrant).
type Metrics struct {
	decisions   *metrics.Counter
	seconds     *metrics.Histogram
	sharedLanes *metrics.Counter
}

// NewMetrics registers the service's metrics on registry.
func NewMetrics(registry *metrics.Registry) *Metrics {
	return &Metrics{
		decisions: registry.NewCounter("broker_nats_callout_decisions_total",
			"NATS connections the auth service decided, by credential kind (session, service, none), outcome (admitted, refused) and the refusal's reason."),
		seconds: registry.NewHistogram("broker_nats_callout_seconds",
			"How long the auth service took to answer one NATS connection from the request's arrival, in seconds. Its buckets "+
				"straddle the decision's 1.5 s budget and the two seconds a client's connect allows.",
			[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 1.5, 2}),
		sharedLanes: registry.NewCounter("broker_nats_callout_shared_lanes_dropped_total",
			"Repositories the auth service withheld from a daemon's grant held to its exchange projects, because another project's mapping in Dispatch names the same GitHub lane."),
	}
}

// withheldSharedLanes counts n repositories withheld from one held daemon's grant (scopeGrant).
func (m *Metrics) withheldSharedLanes(n int) {
	if m == nil {
		return
	}
	m.sharedLanes.Add(int64(n))
}

// record counts one answer to a request whose credential is kind, refused by refused when it is
// not nil, which took took from the request's arrival. The reason label is the refusal's own
// (Refusal.Label), one fixed word per kind of refusal, never read out of its text.
func (m *Metrics) record(kind string, refused *Refusal, took time.Duration) {
	if m == nil {
		return
	}
	outcome, reason := "admitted", ""
	if refused != nil {
		outcome, reason = "refused", refused.Label
	}
	m.decisions.Inc([2]string{"kind", kind}, [2]string{"outcome", outcome}, [2]string{"reason", reason})
	m.seconds.Observe(took.Seconds())
}
