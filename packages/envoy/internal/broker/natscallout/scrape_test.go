package natscallout

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// stalledScrape is a scrape's response whose writes stop at the first line of the metric name's
// series, as a scrape client that stops reading stops the metrics server's writes. The registry
// writes each series line under that metric's series lock (metrics.Registry.Handler), so the
// scrape holds the lock from then on. entered is closed when the write stops, and closing
// released lets it through.
type stalledScrape struct {
	name     string
	header   http.Header
	entered  chan struct{}
	once     sync.Once
	released chan struct{}
}

func (s *stalledScrape) Header() http.Header { return s.header }

func (s *stalledScrape) WriteHeader(int) {}

func (s *stalledScrape) Write(p []byte) (int, error) {
	if bytes.HasPrefix(p, []byte(s.name+"{")) {
		s.once.Do(func() { close(s.entered) })
		<-s.released
	}
	return len(p), nil
}

// A scrape client that stops reading delays no answer. The registry holds a counter's series lock
// across its writes to the scraper (metrics.Registry.Handler), and the metrics server's write
// waits on the client for up to its WriteTimeout; recording the decision takes that lock, so the
// answer goes out first and the decision is recorded after it (answer). One refusal makes the
// decision counter's first series; a scrape then stops on that series' line, holding the lock,
// and the next refusal must be heard at once.
func TestAScrapeThatStopsReadingDelaysNoAnswer(t *testing.T) {
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	registry := metrics.New()
	startService(t, url, keys, Deps{Metrics: NewMetrics(registry)}, Decide)
	requireRefusedAtOnce(t, url, "first")
	testwait.WithinPolling(t, 5*time.Second, 20*time.Millisecond, "the first refusal recorded", func() bool {
		return strings.Contains(scrape(t, registry), "broker_nats_callout_decisions_total{")
	})

	stalled := &stalledScrape{name: "broker_nats_callout_decisions_total", header: http.Header{},
		entered: make(chan struct{}), released: make(chan struct{})}
	// Released before the service's own cleanup closes it, which waits for every answer, the one
	// that records under the lock among them.
	t.Cleanup(func() { close(stalled.released) })
	go registry.Handler().ServeHTTP(stalled, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	select {
	case <-stalled.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the scrape never wrote the decision counter's series")
	}
	requireRefusedAtOnce(t, url, "second")
}
