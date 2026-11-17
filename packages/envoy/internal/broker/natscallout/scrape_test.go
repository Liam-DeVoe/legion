package natscallout

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// stalledScrape is a scrape's response whose gate holds the first write that carries the line of
// the metric name's series, and every write after it: a scrape client that stops reading holds
// the metrics server's writes once the socket's buffers are full, for up to its WriteTimeout.
type stalledScrape struct {
	*writeGate
	name   string
	header http.Header
	held   bool
}

func (s *stalledScrape) Header() http.Header { return s.header }

func (s *stalledScrape) WriteHeader(int) {}

func (s *stalledScrape) Write(p []byte) (int, error) {
	if s.held || bytes.Contains(p, []byte(s.name+"{")) {
		s.held = true
		s.hold()
	}
	return len(p), nil
}

// stallScrape starts one scrape of registry and holds it at the decision counter's first series
// line, once a first decision has made that series. It releases the scrape before the service's
// own cleanup closes the service, which waits for every request.
func stallScrape(t *testing.T, url string, registry *metrics.Registry) {
	t.Helper()
	requireRefusedAtOnce(t, url, "first")
	testwait.WithinPolling(t, 5*time.Second, 20*time.Millisecond, "the first refusal recorded", func() bool {
		return strings.Contains(scrape(t, registry), "broker_nats_callout_decisions_total{")
	})
	stalled := &stalledScrape{writeGate: newWriteGate(), name: "broker_nats_callout_decisions_total", header: http.Header{}}
	t.Cleanup(stalled.release)
	go registry.Handler().ServeHTTP(stalled, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	stalled.awaitHeld(t, "the decision counter's series in the scrape")
}

// A scrape client that stops reading holds no decision slot. A request decided in a slot keeps it
// until answer returns, and answer records the decision (Metrics.record) after it sends it; were
// the registry to hold the decision counter's lock across its write to a scrape, as its handler
// once did, a stopped scrape would hold every request recording behind it in its slot.
// metrics.Registry.Handler renders the text under its locks and writes it holding none. With one
// slot and a scrape stopped on the decision counter's line, a connection holding no credential is
// refused in the slot, and a live session connecting after it must be admitted inside half a
// second, with the replica then holding nothing.
func TestAStalledScrapeHoldsNoDecisionSlot(t *testing.T) {
	withDecisionSlots(t, 1)
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	live := newSession(t, "ses_live", 15*time.Minute)
	registry := metrics.New()
	service := startService(t, url, keys, Deps{Introspect: introspector(live), Metrics: NewMetrics(registry)}, Decide)
	stallScrape(t, url, registry)

	requireRefusedAtOnce(t, url, "second")
	requireLiveAdmittedAtOnce(t, url, live)
	requireHoldsNothing(t, service)
}

// The same at the deployed number of slots: once concurrentDecisions requests were held recording
// behind a stopped scrape, no caller, however live its credential, would be decided. More than
// concurrentDecisions connections holding no credential are each refused at once in a slot while a
// scrape is stopped on the decision counter's line, and then a live session is admitted inside half
// a second, with the replica holding nothing.
func TestAStalledScrapeHoldsNoneOfTheDeployedSlots(t *testing.T) {
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	live := newSession(t, "ses_live", 15*time.Minute)
	registry := metrics.New()
	service := startService(t, url, keys, Deps{Introspect: introspector(live), Metrics: NewMetrics(registry)}, Decide)
	stallScrape(t, url, registry)

	for i := range concurrentDecisions + 6 {
		requireRefusedAtOnce(t, url, fmt.Sprintf("#%d", i+1))
	}
	requireLiveAdmittedAtOnce(t, url, live)
	requireHoldsNothing(t, service)
}

// requireLiveAdmittedAtOnce requires live's connection admitted inside half a second.
func requireLiveAdmittedAtOnce(t *testing.T, url string, live *session) {
	t.Helper()
	began := time.Now()
	nc, err := connectSession(t, url, live, nil)
	took := time.Since(began)
	require.NoError(t, err, "the live session was not admitted (after %s): a slot was held by a refusal recording behind the stopped scrape", took)
	nc.Close()
	require.Less(t, took, 500*time.Millisecond, "the live session was admitted after %s, behind refusals recording behind the stopped scrape", took)
}
