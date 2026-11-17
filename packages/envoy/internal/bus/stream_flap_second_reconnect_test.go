package bus

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/dispatch/benchtime"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// The case beside TestAFlapDuringTheEnsuresRequestRunsTheSecondDisconnectHookAtOnce: the run of
// the reconnect hooks that met the second outage is off nats.go's callback goroutine now, so the
// second disconnect's hooks run at once; but its stream request is still unanswered on the
// connection that dropped. When the connection is back a moment later, the reconnect that brings it
// back is due a run of the hooks of its own, which re-watch the KV caches the second disconnect
// told to stop following and ensure the stream on the server reached. Runs take turns (rewatch),
// so a first run that waits out its request's whole bound (the JetStream context's MaxWait, 10 s)
// holds the second reconnect's run behind it, and every cache answers from a bucket it does not
// follow for that long while the connection is up.
//
// flapProxy (stream_flap_request_test.go) drops the reconnect at its first $JS.API.STREAM request
// and refuses connections for 3 s; nats.go reconnects within a second or so of that. A hook added
// after the client's own records each run; this asks that a run begins once the connection is
// back. How soon it begins is measured on this machine's clock against nats.go's own request timer
// (the first run's MaxWait), which no clock of this test reaches, so "within 3 s" is held only in
// benchtime's timing lane.
func TestASecondReconnectRunsItsHooksWithoutWaitingOutTheFirstRunsRequest(t *testing.T) {
	_, uri := testnats.Start(t)
	proxy := newFlapProxy(t, strings.TrimPrefix(uri, "nats://"))
	client, err := ConnectOwningStream([]string{"nats://" + proxy.addr()}, WithName(t.Name()))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	var mu sync.Mutex
	var runs []time.Time
	client.AddReconnectHook(func(context.Context, *nats.Conn) error {
		mu.Lock()
		runs = append(runs, time.Now())
		mu.Unlock()
		return nil
	})

	proxy.cutAndArm(time.Second)
	dropped := testwait.Receive(t, "a stream request of the reconnect's ensure to reach the proxy, which drops it", proxy.dropped)
	// The connection is back once the proxy's 3 s refusal after the drop has ended and nats.go has
	// reconnected; one read CONNECTED before then is the connection the drop closed.
	back := testwait.Until(t, "the connection to be back once the proxy's refusal ended", func() (time.Time, bool) {
		now := time.Now()
		conn := client.Connection()
		return now, conn != nil && conn.IsConnected() && now.Sub(dropped) > 3*time.Second
	})
	at := testwait.Until(t, "a run of the hooks once the connection was back", func() (time.Time, bool) {
		mu.Lock()
		defer mu.Unlock()
		for _, at := range runs {
			if at.After(back) || at.After(dropped.Add(3*time.Second)) {
				return at, true
			}
		}
		return time.Time{}, false
	})
	late := at.Sub(back)
	t.Logf("the hooks ran %s after the connection was back", late)
	if os.Getenv(benchtime.TimingEnv) != "" && late > 3*time.Second {
		t.Fatalf("the hooks first ran %s after the connection was back (%s after the second outage began), want within 3s: the reconnect's run waited for the first run's unanswered request", late, at.Sub(dropped))
	}
}
