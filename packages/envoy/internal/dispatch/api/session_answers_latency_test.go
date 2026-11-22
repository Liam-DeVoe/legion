package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/sessionanswers"
	"github.com/sjawhar/envoy/internal/testwait"
)

// TestTheAnswersReadIsTimedAsTheAccessLogTimesIt reads a session's answers through the route, on
// the package's test server with a sessionanswers.Latency in its dependencies whose clock hands
// the test each instant it reads: a read's begin and its end. A fast read is timed, and is no slow
// read. A read that a transaction holds for 900 ms, by its lock on message_deliveries, which the
// answers statement reads, is a slow read while it runs past 800 ms and, once it ended, for the
// 10 s after its end. Each read's time lies inside the time its access-log line records, but for
// the line's truncation to the millisecond, the line and the Latency agree on whether the read was
// slow, and a read of another route is not timed.
func TestTheAnswersReadIsTimedAsTheAccessLogTimesIt(t *testing.T) {
	_, database, deps := newTestServer(t, testServerOptions{})
	instants := make(chan time.Time, 8)
	latency := sessionanswers.NewLatency(func() time.Time {
		now := time.Now()
		instants <- now
		return now
	})
	deps.AnswersLatency = latency
	mux := http.NewServeMux()
	served := register(mux, t.Context(), deps)
	t.Cleanup(func() { waitBackgroundOf(t, served) })
	logged := captureLog(t)
	const answers = "/api/v1/agents/" + answersSession + "/answers"

	// An unheld read is fast unless the machine keeps it past SlowRead, and a read the machine
	// kept is a slow read by the Latency's own measure, which shows nothing of the fast case. So
	// the read is made again until the Latency times one at or under SlowRead, each read held to
	// its access-log line as it goes; lastSlowEnd is the end of the latest read the machine kept.
	var began, ended, lastSlowEnd time.Time
	reads := 0
	testwait.UntilPolling(t, 20*time.Millisecond, "an unheld answers read the Latency times at or under SlowRead (the time the last one took)", func() (time.Duration, bool) {
		if response := dispatchRequest(t, mux, http.MethodGet, answers, nil, "alice"); response.Code != http.StatusOK {
			t.Fatalf("the unheld read answered %d: %s", response.Code, response.Body.String())
		}
		began, ended = nextInstant(t, instants, "an unheld read's begin"), nextInstant(t, instants, "an unheld read's end")
		took := ended.Sub(began)
		requireTimedAsLogged(t, logged, reads, took)
		reads++
		if took > sessionanswers.SlowRead {
			t.Logf("unheld answers read %d took %v, over %v, so it shows no fast read: the read is made again", reads, took, sessionanswers.SlowRead)
			lastSlowEnd = ended
			return took, false
		}
		return took, true
	})
	// A read the machine kept makes Slow answer yes for its own sake until SlowWithin after its
	// end, so a question about another read is asked no earlier than that.
	pastKept := func(at time.Time) time.Time {
		if !lastSlowEnd.IsZero() && at.Before(lastSlowEnd.Add(sessionanswers.SlowWithin)) {
			return lastSlowEnd.Add(sessionanswers.SlowWithin)
		}
		return at
	}
	if latency.Slow(pastKept(ended)) {
		t.Error("a fast read that ended is a slow read")
	}

	holder, err := database.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the transaction that holds the read: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(t.Context(), `lock table message_deliveries in access exclusive mode`); err != nil {
		t.Fatalf("lock message_deliveries: %v", err)
	}
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() { responses <- dispatchRequest(t, mux, http.MethodGet, answers, nil, "alice") }()
	began = nextInstant(t, instants, "the held read's begin")
	waitForDatabaseLocks(t, holder, 1)
	time.Sleep(time.Until(began.Add(900 * time.Millisecond)))
	if !latency.Slow(pastKept(time.Now())) {
		t.Error("a read that has run 900 ms and not ended is no slow read")
	}
	if err := holder.Rollback(t.Context()); err != nil {
		t.Fatalf("release message_deliveries: %v", err)
	}
	if response := awaitResponse(t, responses); response.Code != http.StatusOK {
		t.Fatalf("the held read answered %d: %s", response.Code, response.Body.String())
	}
	ended = nextInstant(t, instants, "the held read's end")
	if took := ended.Sub(began); took < 900*time.Millisecond {
		t.Fatalf("the held read was timed %v; the lock held it 900 ms", took)
	}
	if !latency.Slow(ended.Add(sessionanswers.SlowWithin - time.Millisecond)) {
		t.Errorf("a read that took over %v is no slow read just under %v after its end", sessionanswers.SlowRead, sessionanswers.SlowWithin)
	}
	if latency.Slow(ended.Add(sessionanswers.SlowWithin)) {
		t.Errorf("a read that took over %v is still a slow read %v after its end", sessionanswers.SlowRead, sessionanswers.SlowWithin)
	}
	requireTimedAsLogged(t, logged, reads, ended.Sub(began))

	if response := dispatchRequest(t, mux, http.MethodGet, "/api/v1/agents/"+answersSession+"/messages", nil, "alice"); response.Code != http.StatusOK {
		t.Fatalf("the session's messages answered %d: %s", response.Code, response.Body.String())
	}
	select {
	case instant := <-instants:
		t.Errorf("a read of the session's messages was timed (at %v)", instant)
	default:
	}
}

// nextInstant is the next instant the Latency's clock read. Its one bound is the test binary's
// deadline less testwait's margin.
func nextInstant(t *testing.T, instants <-chan time.Time, what string) time.Time {
	t.Helper()
	return testwait.Receive(t, "the Latency to read "+what, instants)
}

// elapsedField reads an access-log line's elapsed_ms.
var elapsedField = regexp.MustCompile(`\belapsed_ms=(\d+)\b`)

// requireTimedAsLogged fails unless the answers read the access log logged n-th (from 0), which
// the Latency timed as timed, agrees with its line. The Latency times the read inside the line's
// own timing (routeHandler), so its time is at most the line's. And the two agree on whether the
// read was slow at the product's own bound, sessionanswers.SlowRead, in both directions: the
// keyword fill pauses while Dispatch takes more than SlowRead to answer an answers read, which the
// access-log line times, so a read its line times over SlowRead is a slow read to the Latency, and
// one the Latency counts slow its line times at SlowRead or more. Each side is read from its own
// record: the line's elapsed_ms and the Latency's two readings. A line at SlowRead to the
// millisecond can be either. A correct run fails this only if the machine parks a read across
// SlowRead between the two timers.
func requireTimedAsLogged(t *testing.T, logged *logBuffer, n int, timed time.Duration) {
	t.Helper()
	var reads []string
	for _, line := range accessLines(t, logged) {
		if strings.Contains(line, "route=/api/v1/agents/{session_id}/answers ") {
			reads = append(reads, line)
		}
	}
	if len(reads) <= n {
		t.Fatalf("the access log holds %d answers reads; want a line for read %d:\n%s", len(reads), n, strings.Join(reads, "\n"))
	}
	match := elapsedField.FindStringSubmatch(reads[n])
	if match == nil {
		t.Fatalf("the access-log line %q carries no elapsed_ms", reads[n])
	}
	elapsed, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		t.Fatalf("read elapsed_ms of %q: %v", reads[n], err)
	}
	if ms := timed.Milliseconds(); ms > elapsed {
		t.Errorf("answers read %d was timed %v; its access-log line records elapsed_ms=%d, less than that", n, timed, elapsed)
	}
	line := time.Duration(elapsed) * time.Millisecond
	switch slow := timed > sessionanswers.SlowRead; {
	case slow && line < sessionanswers.SlowRead:
		t.Errorf("answers read %d was a slow read to the Latency (%v) and its access-log line times it under %v (elapsed_ms=%d)",
			n, timed, sessionanswers.SlowRead, elapsed)
	case !slow && line > sessionanswers.SlowRead:
		t.Errorf("answers read %d took %v by its access-log line (elapsed_ms=%d), over %v, and the Latency timed it %v, no slow read",
			n, line, elapsed, sessionanswers.SlowRead, timed)
	}
}
