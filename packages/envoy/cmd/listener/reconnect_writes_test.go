package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/testnats"
)

// reconnectBufferRefusal is the text of nats.go's refusal of a send while it reconnects with the
// reconnect buffer off (nats.ErrReconnectBufExceeded). A standing session's role claim refresh
// answered it during the hub's NATS restart on 2026-11-16, as a 503 that named a buffer and no
// reconnect.
const reconnectBufferRefusal = "outbound buffer limit exceeded"

// reconnectingReason is what a 503 says when the bus is reconnecting (bus.ErrReconnecting).
const reconnectingReason = "NATS is reconnecting"

// reconnectWritesAnswer is one route's answer to the test: its status and body, or the error of a
// request the test gave up on itself.
type reconnectWritesAnswer struct {
	status int
	body   string
	err    error
}

// String says the answer for a failure message.
func (a reconnectWritesAnswer) String() string {
	if a.err != nil {
		return "no answer: " + a.err.Error()
	}
	return fmt.Sprintf("%d %s", a.status, strings.TrimSpace(a.body))
}

// reconnectWritesCall posts body to path on the listener at port as the shared-token caller, and
// gives up on the answer after timeout. It reports a failure rather than failing the test, so it
// runs on a goroutine of its own.
func reconnectWritesCall(port int, path, body string, timeout time.Duration) reconnectWritesAnswer {
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(port)+path, strings.NewReader(body))
	if err != nil {
		return reconnectWritesAnswer{err: err}
	}
	request.Header.Set("Authorization", "Bearer "+listenerTestToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return reconnectWritesAnswer{err: err}
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	return reconnectWritesAnswer{status: response.StatusCode, body: string(answer), err: err}
}

// A write the listener makes while its NATS connection is reconnecting waits for the connection
// within its caller's deadline and is then sent, or fails by name once that deadline ends: the
// answer is a 503 whose reason says the bus is reconnecting. No answer and no log line holds
// nats.go's "outbound buffer limit exceeded", which the client's reconnect buffer, kept off so
// that nothing is sent after its caller saw an error, makes nats.go answer to every send while it
// reconnects. And no write the caller gave up on reaches the server once it is back: neither a
// role claim, nor the session record a claim refreshes, nor a publish.
//
// The server is stopped twice. The first outage is shorter than the routes' deadline, so each
// write may wait for the server and land; the second is longer, so each must fail by name and
// leave nothing behind.
func TestAWriteThroughANATSReconnectWaitsOrFailsByName(t *testing.T) {
	const (
		machineID = "reconnect-writes"
		sessionID = "ses_reconnect_writes"
		// The routes' own deadline is 3.5 s; a caller that gives up sooner gives up at this.
		givenUpAfter = time.Second
		// The client waits this long for the routes' answers, past their own deadline.
		answerWithin = 15 * time.Second
	)
	server, uri := testnats.Start(t)
	listener := startListenerProcess(t, cmdtest.Build(t, "envoy-listener"), uri, machineID)
	listener.waitHealthy(t)
	postListener(t, listener.port, "/v1/interests/subscribe", consumerRegistration(sessionID, "epoch-1"))
	postListener(t, listener.port, "/v1/roles/set", `{"session_id":"`+sessionID+`","role":"reconnect-writes-before"}`)

	claim := func(role string) string {
		return `{"session_id":"` + sessionID + `","role":"` + role + `"}`
	}
	publishBody := func(topic string) string {
		return `{"topic":"` + topic + `","message":"a publish through a NATS reconnect"}`
	}
	// lines counts the main client's lines of kind (bus.Client's connection callbacks, in Go's text
	// format); the role lane's connection logs the same lines under name=envoy-role-lane.
	lines := func(kind string) int {
		return strings.Count(listener.Output.String(), "envoy nats "+kind+" name=envoy ")
	}
	// stop stops the server and returns once the listener's client has seen it go.
	stop := func(t *testing.T) {
		t.Helper()
		disconnects := lines("disconnected")
		server.Stop(t)
		waitFor(t, 15*time.Second, "the listener to see the NATS server go", func() bool { return lines("disconnected") > disconnects })
	}
	// observe reads the server the listener writes to, on a connection of the test's own.
	observe := func(t *testing.T) natsgo.JetStreamContext {
		t.Helper()
		conn := testnats.Connect(t, uri)
		t.Cleanup(conn.Close)
		js, err := conn.JetStream()
		if err != nil {
			t.Fatalf("open JetStream to read what the listener wrote: %v", err)
		}
		return js
	}
	bucket := func(t *testing.T, js natsgo.JetStreamContext, name string) natsgo.KeyValue {
		t.Helper()
		kv, err := js.KeyValue(name)
		if err != nil {
			t.Fatalf("open the %s bucket: %v", name, err)
		}
		return kv
	}
	var answers []reconnectWritesAnswer
	var answersMu sync.Mutex
	// call makes calls at once, each on a goroutine of its own, and returns their answers in order
	// once all have answered.
	call := func(calls ...func() reconnectWritesAnswer) []reconnectWritesAnswer {
		got := make([]reconnectWritesAnswer, len(calls))
		var wg sync.WaitGroup
		for i, c := range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got[i] = c()
			}()
		}
		wg.Wait()
		answersMu.Lock()
		answers = append(answers, got...)
		answersMu.Unlock()
		return got
	}
	// waitedOrNamed fails the test unless answer is a success or a 503 naming the reconnect.
	waitedOrNamed := func(t *testing.T, what string, answer reconnectWritesAnswer) {
		t.Helper()
		if answer.err != nil || (answer.status != http.StatusOK && (answer.status != http.StatusServiceUnavailable || !strings.Contains(answer.body, reconnectingReason))) {
			t.Errorf("%s during the outage answered %s, want 200 once the server was back or 503 naming %q", what, answer, reconnectingReason)
		}
	}

	t.Run("a role claim refresh and a publish during an outage wait for the server or fail by name", func(t *testing.T) {
		const role, topic = "reconnect-writes-waited", "notifications.team.reconnect-writes-waited"
		stop(t)
		restarted := make(chan struct{})
		go func() {
			defer close(restarted)
			time.Sleep(time.Second)
			server.Start(t)
		}()
		got := call(
			func() reconnectWritesAnswer {
				return reconnectWritesCall(listener.port, "/v1/roles/set", claim(role), answerWithin)
			},
			func() reconnectWritesAnswer {
				return reconnectWritesCall(listener.port, "/v1/messages/publish", publishBody(topic), answerWithin)
			},
		)
		<-restarted
		t.Logf("during a 1 s outage the role claim refresh answered %s and the publish %s", got[0], got[1])
		waitedOrNamed(t, "the role claim refresh", got[0])
		waitedOrNamed(t, "the publish", got[1])
		js := observe(t)
		if got[0].status == http.StatusOK {
			if _, err := bucket(t, js, bus.RoleClaimBucket).Get(role); err != nil {
				t.Errorf("the role claim answered 200 is not in the role bucket: %v", err)
			}
		}
		if got[1].status == http.StatusOK {
			if _, err := js.GetLastMsg(bus.Stream, topic); err != nil {
				t.Errorf("the publish answered 200 is not in the stream: %v", err)
			}
		}
	})

	t.Run("a role claim refresh and a publish whose caller gives up in an outage fail by name and land nothing", func(t *testing.T) {
		const (
			role, topic = "reconnect-writes-ended", "notifications.team.reconnect-writes-ended"
			abandoned   = "reconnect-writes-abandoned"
		)
		listener.waitHealthy(t)
		js := observe(t)
		sessions := bucket(t, js, bus.SessionBucket)
		before, err := sessions.Get(sessionID)
		if err != nil {
			t.Fatalf("read the session record before the outage: %v", err)
		}
		reconnects := lines("reconnected")
		stop(t)
		got := call(
			func() reconnectWritesAnswer {
				return reconnectWritesCall(listener.port, "/v1/roles/set", claim(role), answerWithin)
			},
			func() reconnectWritesAnswer {
				return reconnectWritesCall(listener.port, "/v1/messages/publish", publishBody(topic), answerWithin)
			},
			func() reconnectWritesAnswer {
				return reconnectWritesCall(listener.port, "/v1/roles/set", claim(abandoned), givenUpAfter)
			},
		)
		// The routes' deadline has ended by now, and the abandoned claim's request was cancelled
		// two and a half seconds ago: nothing of the three may be sent once the server is back.
		t.Logf("in an outage past the routes' deadline the role claim refresh answered %s, the publish %s, and the claim given up on %s", got[0], got[1], got[2])
		for i, what := range []string{"the role claim refresh", "the publish"} {
			if got[i].status != http.StatusServiceUnavailable || !strings.Contains(got[i].body, reconnectingReason) {
				t.Errorf("%s whose deadline ended in the outage answered %s, want 503 naming %q", what, got[i], reconnectingReason)
			}
		}
		if got[2].err == nil {
			t.Errorf("the claim the test gave up on after %s answered %s before the test gave up on it", givenUpAfter, got[2])
		}
		server.Start(t)
		waitFor(t, 30*time.Second, "the listener to reconnect", func() bool { return lines("reconnected") > reconnects })
		listener.waitHealthy(t)
		// A write held anywhere and sent after the reconnect would be on the server within this.
		time.Sleep(2 * time.Second)

		js = observe(t)
		roles := bucket(t, js, bus.RoleClaimBucket)
		for _, r := range []string{role, abandoned} {
			if entry, err := roles.Get(r); !errors.Is(err, natsgo.ErrKeyNotFound) {
				t.Errorf("role %s's claim, whose caller had given up, reached the server after it came back: %v, %v", r, entry, err)
			}
		}
		if after, err := bucket(t, js, bus.SessionBucket).Get(sessionID); err != nil || after.Revision() != before.Revision() {
			t.Errorf("the session record a given-up claim refreshes moved from revision %d during the outage: %v, %v", before.Revision(), after, err)
		}
		if message, err := js.GetLastMsg(bus.Stream, topic); !errors.Is(err, natsgo.ErrMsgNotFound) {
			t.Errorf("the publish whose caller had given up reached the stream after the server came back: %v, %v", message, err)
		}
	})

	t.Run("no answer or log line holds nats.go's buffer refusal", func(t *testing.T) {
		answersMu.Lock()
		defer answersMu.Unlock()
		for _, answer := range answers {
			if strings.Contains(answer.body, reconnectBufferRefusal) {
				t.Errorf("an answer names nats.go's buffer refusal rather than the reconnect: %s", answer)
			}
		}
		for line := range strings.SplitSeq(listener.Output.String(), "\n") {
			if strings.Contains(line, reconnectBufferRefusal) {
				t.Errorf("a log line names nats.go's buffer refusal rather than the reconnect: %s", line)
			}
		}
	})
}
