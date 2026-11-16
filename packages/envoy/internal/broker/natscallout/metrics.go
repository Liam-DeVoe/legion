package natscallout

import (
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/metrics"
)

// Metrics counts the service's decisions: broker_nats_callout_decisions_total by credential kind,
// outcome and reason, which is the production census of the services the hub admits (its
// kind="service" series), and broker_nats_callout_seconds, how long each decision took.
type Metrics struct {
	decisions *metrics.Counter
	seconds   *metrics.Histogram
}

// NewMetrics registers the service's metrics on registry.
func NewMetrics(registry *metrics.Registry) *Metrics {
	return &Metrics{
		decisions: registry.NewCounter("broker_nats_callout_decisions_total",
			"NATS connections the auth service decided, by credential kind (session, service, none), outcome (admitted, refused) and the refusal's reason."),
		seconds: registry.NewHistogram("broker_nats_callout_seconds",
			"How long the auth service took to decide one NATS connection, in seconds. Its buckets "+
				"straddle the decision's 1.5 s budget and the two seconds a client's connect allows.",
			[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 1.5, 2}),
	}
}

// record counts one decision of a request whose credential is kind, refused with reason when it
// is not empty.
func (m *Metrics) record(kind, reason string, took time.Duration) {
	if m == nil {
		return
	}
	outcome := "admitted"
	if reason != "" {
		outcome = "refused"
	}
	m.decisions.Inc([2]string{"kind", kind}, [2]string{"outcome", outcome}, [2]string{"reason", reasonLabel(reason)})
	m.seconds.Observe(took.Seconds())
}

// reasonLabel is a refusal's reason as the counter labels it: the reason up to the account or
// grant entry it names after ": ", so the series stay as few as the reasons, and whole for a proof
// refusal, whose claim is one of a few words.
func reasonLabel(reason string) string {
	switch {
	case strings.HasPrefix(reason, "decision exceeded its "):
		return "budget"
	case strings.HasPrefix(reason, "proof invalid: "):
		return reason
	}
	class, _, _ := strings.Cut(reason, ": ")
	return class
}
