package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/clock/clocktest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/internal/testwait"
)

// scrape answers registry's /metrics body.
func scrape(t *testing.T, registry *metrics.Registry) string {
	t.Helper()
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return response.Body.String()
}

// A mention's send that waits behind an earlier event shows, since nothing bounds the wait: the
// server's gauges count it and read how long the oldest has waited, naming no owner, since
// /metrics answers whoever reaches the port; and past the warn threshold - shortened here to a
// second - the sender logs one WARN naming the owner, the event it waits behind and that event's
// last publish error. The cause is logged too: the outbox's own ERROR for the earlier event's
// failed topic send names that event and its topic. While it waits, each try of the parked lane
// reads the rule alone: no claim, resolution or settle of the attempt is written (a traced pool
// counts the server's statements), and each park waits at least its mentionOrderRetry. Once the
// topic family is captured again the send lands, the gauges read none and no second WARN comes.
// The controls are the scrape before the comment and the one after the send landed.
//
// The sender's parks, and the moments it stamps and judges a held send with, run on a fake that
// reads the wall clock plus what the test adds, as the server stamps a send's first wait on the
// wall clock (sendCommentMention); a park's timer fires only when the test moves the fake to it,
// so the test drives each try of the parked lane itself and counts what each one sends.
func TestAHeldMentionSendIsCountedAndReportedOnce(t *testing.T) {
	logs := &storetest.LockedLog{}
	logtest.SetDefault(t, slog.NewTextHandler(logs, nil))
	database := storetest.Open(t)
	recorder := &storetest.StatementRecorder{}
	registry := metrics.New()
	listener, state := newMentionListener(t)
	handler, _, deps, served := newRegisteredTestServer(t, testServerOptions{
		envoyURL: listener.URL, outboxPublishes: true, metrics: registry,
		store: storetest.TracedStore(t, database, recorder),
	})
	parks := clocktest.Held()
	served.mentions.mu.Lock()
	served.mentions.heldWarnAfter = time.Second
	served.mentions.clock = parks
	served.mentions.mu.Unlock()
	issue := createInteractionIssue(t, handler, "TEST", "A held mention", "before")
	view := &sessionView{issue: issue.Key}
	state.mu.Lock()
	state.landed = view.mentionLanded
	state.mu.Unlock()
	// The outbox runs on a pool of its own, as cmd/dispatch's does, so the traced pool counts the
	// server's statements alone.
	outboxDeps := deps
	outboxDeps.Store = database
	runOutboxBeside(t, outboxDeps, view)
	testwait.Eventually(t, "the issue's first events published", func() bool {
		return unpublishedEvents(t, database, issue.Key) == 0
	})
	if body := scrape(t, registry); !strings.Contains(body, "envoy_dispatch_mention_sends_held 0\n") {
		t.Fatalf("the gauges read %q before any mention, want no held send", heldLines(body))
	}

	view.refuseTopics(true)
	firstComment := decodeMentionedComment(t, sessionRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/comments", map[string]any{
		"body": "event 1, by a session", "actor": map[string]any{"kind": "session", "id": "s2"},
	}))
	first := commentEventID(t, database, firstComment.ID)
	testwait.Eventually(t, "event 1's topic send backed off", func() bool {
		attempts, _ := eventAttempts(t, database, first)
		return attempts > 0
	})
	mention := decodeMentionedComment(t, postMentionedComment(t, handler, issue.Key, map[string]any{
		"body": "the comment that mentions s1", "mentions": []map[string]any{{"target": "session:s1"}},
	}))
	const warning = `level=WARN msg="dispatch: a comment mention's send waits behind an earlier event of its owner"`
	deadline := parks.AwaitArmed(t, "the held send's first park", 1)[0]
	if body := scrape(t, registry); !strings.Contains(body, "envoy_dispatch_mention_sends_held 1\n") {
		t.Fatalf("the gauges read %q once the send parked, want one held send", heldLines(body))
	}
	if got := strings.Count(logs.String(), warning); got != 0 {
		t.Fatalf("%d WARN lines before the send waited its threshold, want none", got)
	}

	// Eight tries of the parked lane, each when the fake reaches its park's deadline, which puts
	// the waits past mentionOrderRetryMax and the send past the threshold: count what the server
	// sends the database meanwhile. A park is armed after its try's statements, so the next
	// park's deadline says the try before it is done.
	const tries = 8
	before := len(recorder.Statements())
	for try := 1; try <= tries; try++ {
		parks.Set(deadline)
		next := parks.AwaitArmed(t, fmt.Sprintf("the held lane's park after try %d", try), 1)[0]
		// The park is armed at its try's end, a moment the fake reads past deadline, so its wait
		// is at least the gap; a park shorter than its mentionOrderRetry would read the rule more
		// often than the cost bound says. The first park's arming is no deadline the test saw.
		if want := mentionOrderRetry(try + 1); next.Sub(deadline) < want {
			t.Errorf("park %d of the held lane waits %v, want at least mentionOrderRetry (%v)", try+1, next.Sub(deadline), want)
		}
		deadline = next
	}
	window := recorder.Statements()[before:]
	var reads, attemptWrites int
	for _, statement := range window {
		switch {
		case strings.Contains(statement, "k.owed_keys &&"):
			reads++
		case strings.Contains(statement, "comment_deliveries"):
			attemptWrites++
		}
	}
	body := scrape(t, registry)
	t.Logf("over %d tries parked: %d reads of the rule, %d statements on comment_deliveries, %d statements in all; gauges:\n%s",
		tries, reads, attemptWrites, len(window), heldLines(body))
	if reads != tries {
		t.Errorf("the parked lane read the rule %d times over %d tries, want once a try", reads, tries)
	}
	if attemptWrites != 0 {
		t.Errorf("the parked lane made %d statements on comment_deliveries over %d tries, want none: a try of a held send reads the rule alone", attemptWrites, tries)
	}
	if !strings.Contains(body, "envoy_dispatch_mention_send_held_oldest_seconds ") ||
		strings.Contains(body, "envoy_dispatch_mention_send_held_oldest_seconds 0\n") {
		t.Errorf("the gauges read %q, want the held send's age past zero seconds", heldLines(body))
	}
	if lines := heldLines(body); strings.Contains(lines, "{") || strings.Contains(lines, issue.Key) {
		t.Errorf("the gauges read %q, want no label and no owner's key: /metrics answers whoever reaches the port", lines)
	}
	logged := logs.String()
	if got := strings.Count(logged, warning); got != 1 {
		t.Errorf("%d WARN lines for the held send, want one", got)
	}
	for _, want := range []string{"owner=" + issue.Key, fmt.Sprintf("event_id=%d", first), "session=s1",
		"publish_error=", "notifications.dispatch.issue." + issue.Key + ".comment.created"} {
		if !strings.Contains(warningLine(logged, warning), want) {
			t.Errorf("the WARN line does not name %q: %s", want, warningLine(logged, warning))
		}
	}
	// The cause: the outbox's ERROR for event 1's topic send, naming the event and its topic.
	cause := warningLine(logged, `level=ERROR msg="dispatch outbox: publish event"`)
	if !strings.Contains(cause, fmt.Sprintf("event_id=%d", first)) || !strings.Contains(cause, "publish owner topic") {
		t.Errorf("the outbox's ERROR for event 1's failed topic send is %q, want one naming event %d and its owner topic", cause, first)
	}

	// The outbox publishes event 1 on its own retry; each try the lane makes until then parks
	// again, and the test moves the fake to each park in turn.
	view.refuseTopics(false)
	testwait.Eventually(t, "the held send landed", func() bool {
		attempts := waitForCommentDeliveries(t, database, mention.ID, "the mention's attempts", func([]model.CommentDelivery) bool { return true })
		if len(attempts) == 1 && attempts[0].State == "sent" {
			return true
		}
		if armed := parks.Armed(); len(armed) > 0 {
			parks.Set(armed[0])
		}
		return false
	})
	if body := scrape(t, registry); !strings.Contains(body, "envoy_dispatch_mention_sends_held 0\n") {
		t.Fatalf("the gauges read %q once the send landed, want no held send", heldLines(body))
	}
	if got := strings.Count(logs.String(), warning); got != 1 {
		t.Fatalf("%d WARN lines in all, want the one", got)
	}
}

// heldLines is the held gauges' lines of a /metrics body.
func heldLines(body string) string {
	var lines []string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "envoy_dispatch_mention_send") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// warningLine is the first line of logged holding marker, "" for none.
func warningLine(logged, marker string) string {
	for line := range strings.SplitSeq(logged, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	return ""
}
