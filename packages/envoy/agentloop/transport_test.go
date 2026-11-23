package agentloop

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/logtest"
)

// TestRetryableBoundsTheWaitTheEndpointNames: the one retry of a model call reads the wait the
// endpoint names by the one rule every reader of a server's wait in this module keeps
// (internal/serverwait). A Retry-After of 9999999999 seconds and an X-RateLimit-Reset of
// math.MaxInt64 with no requests left are past what a Duration holds and read as one hour, which
// a run bounded under an hour, such as the judge's 20 s modelBound, has no time for, so the call
// is not made again (this test's run has two minutes): converted as it came, the count read as a
// negative wait, which any run had time for, and the call was made again at once. A negative
// Retry-After reads as no wait, an ordinary value of each header as itself, and an answer naming
// none waits defaultRetryWait. The test counts the rows it drove and fails under the number the
// table must hold.
func TestRetryableBoundsTheWaitTheEndpointNames(t *testing.T) {
	const rowsToDrive = 6
	now := time.Now()
	exhausted := func(reset string) http.Header {
		return http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {reset}}
	}
	rows := []struct {
		name     string
		status   int
		header   http.Header
		min, max time.Duration
		// again is whether a run with two minutes left waits the wait and makes the call again.
		again bool
	}{
		{"a 429 asking 9999999999 seconds", http.StatusTooManyRequests, http.Header{"Retry-After": {"9999999999"}}, time.Hour, time.Hour, false},
		{"a 429 whose X-RateLimit-Reset is math.MaxInt64 with no requests left", http.StatusTooManyRequests, exhausted(strconv.FormatInt(math.MaxInt64, 10)), time.Hour, time.Hour, false},
		{"a 429 asking a negative wait", http.StatusTooManyRequests, http.Header{"Retry-After": {"-30"}}, 0, 0, true},
		{"a 429 asking 30 seconds", http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, 30 * time.Second, 30 * time.Second, true},
		// The reset is whole seconds, and retryable reads it against its own clock a moment
		// later, so the wait is up to two seconds short of the 90 it names.
		{"a 503 whose X-RateLimit-Reset is 90 seconds out with no requests left", http.StatusServiceUnavailable, exhausted(strconv.FormatInt(now.Add(90*time.Second).Unix(), 10)), 88 * time.Second, 90 * time.Second, true},
		{"a 503 naming no wait", http.StatusServiceUnavailable, http.Header{}, defaultRetryWait, defaultRetryWait, true},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	driven := 0
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			driven++
			wait, retry := retryable(&http.Response{StatusCode: row.status, Header: row.header}, nil)
			if !retry || wait < row.min || wait > row.max {
				t.Errorf("retryable = %s (retry %t), want %s to %s and a retry", wait, retry, row.min, row.max)
			}
			if again := waitable(ctx, wait); again != row.again {
				t.Errorf("a run with two minutes left makes the call again after %s: %t, want %t", wait, again, row.again)
			}
		})
	}
	if driven < rowsToDrive {
		t.Fatalf("the table drove %d rows, want %d", driven, rowsToDrive)
	}
}

// TestA429IsLoggedOnceForTheAttemptItAnswered: a 429 the model service answers is logged at WARN
// for the attempt it answered, once, with the deployment the request names, the attempt, the wait
// before the retry, here the Retry-After of 1 the service gave, and that the call is made again;
// the retry's answer is the call's. The test runs alone, not in parallel, so the default logger it
// reads holds no other test's lines.
func TestA429IsLoggedOnceForTheAttemptItAnswered(t *testing.T) {
	logs := &logtest.Recorder{}
	logtest.SetDefault(t, logs)
	limited := reply{
		status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "1"},
		body: []byte(`{"error":{"code":"429","message":"Requests to the deployment have exceeded its rate limit."}}`),
	}
	s := newStandIn(t, limited, chatStream("stop", chatUsage{Prompt: 2, Completion: 3}, "The note says plum."))
	f := newFixture(t, s, OpenAIChat, 1)
	f.req.Model.ID = "chat-deployment-standin"
	result, err := Run(context.Background(), f.req)
	if err != nil || result.Fault != "" || result.Text != "The note says plum." {
		t.Fatalf("after a 429: result %+v, %v; want the retry's answer", result, err)
	}
	if n := len(s.attempts()); n != 2 {
		t.Errorf("a 429 then an answer took %d requests, want 2", n)
	}
	var warned []map[string]any
	for _, record := range logs.Records() {
		if record.Level != slog.LevelWarn {
			continue
		}
		attrs := map[string]any{"msg": record.Message}
		record.Attrs(func(attr slog.Attr) bool {
			attrs[attr.Key] = attr.Value.Any()
			return true
		})
		warned = append(warned, attrs)
	}
	want := []map[string]any{{
		"msg":        "agentloop: the model service answered 429, its rate limit",
		"deployment": "chat-deployment-standin", "attempt": int64(1), "wait": time.Second, "retry": true,
	}}
	if !reflect.DeepEqual(warned, want) {
		t.Errorf("the run logged the warnings %v, want the one line of its 429 %v", warned, want)
	}
}
