package natscallout

import (
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
)

// stalledLog is a log sink that stops taking writes once stalled is set, as the broker's stdout
// does when the reader of the pod's log pipe falls behind (the deliver comment's "a log line is a
// write, which can block"); closing released lets every blocked write through.
type stalledLog struct {
	stalled  atomic.Bool
	released chan struct{}
}

func (l *stalledLog) Write(p []byte) (int, error) {
	if l.stalled.Load() {
		<-l.released
	}
	return len(p), nil
}

// startWithStalledLog starts the service on url with a log that stalls once the test sets its
// stalled flag, and releases the log before the service's own cleanup closes the service, which
// waits for every refusal.
func startWithStalledLog(t *testing.T, url string, keys testnats.CalloutKeys, deps Deps) *stalledLog {
	t.Helper()
	log := &stalledLog{released: make(chan struct{})}
	deps.Log = slog.New(slog.NewTextHandler(log, nil))
	startService(t, url, keys, deps, Decide)
	t.Cleanup(func() { close(log.released) })
	return log
}

// requireRefusedAtOnce connects once with no credential and a client that would wait five
// seconds, and requires the refusal inside half a second.
func requireRefusedAtOnce(t *testing.T, url, which string) {
	t.Helper()
	began := time.Now()
	nc, err := nats.Connect(url, nats.NoReconnect(), nats.Timeout(5*time.Second))
	took := time.Since(began)
	if err == nil {
		nc.Close()
	}
	require.ErrorIs(t, err, nats.ErrAuthorization, "the %s connection holding no credential was not refused (after %s)", which, took)
	require.Less(t, took, 500*time.Millisecond,
		"the %s refusal was heard after %s: nothing answered it, and the hub refused the client at its own timeout", which, took)
}

// A refused client hears its refusal at once even while the broker's log cannot be written. The
// waiting bound refuses rather than drops so that "its client hears at once, and why"
// (maxRefusing), and refusals left the delivery goroutine because a log line is a write that can
// block (deliver). At c24391bbc6 answer wrote the refusal's line before it sent the
// answer, so while the log was stalled a refusal waited on its line and its client heard nothing
// until the hub refused it at its own two-second timeout; and the refusing bound then counted
// refusals stuck on the log, so once maxRefusing of them were stuck every request past the
// waiting bound was dropped, from a replica with nothing else to do. On the real stack (a broker
// whose stdout reader took 5 KB/s, 3000 session connections at once) 1401 of the 3000, and 47 of 78
// connections holding no credential beside them, waited out the client's 2 s, and 471 were dropped
// past_bounds; the same run against the refusal on the delivery goroutine (8ed6996826's) answered
// all but none late. Now the answer goes out first and the refusal is only counted (answer).
//
// Each row stalls the log and then connects with no credential. The last row's bound is one
// refusal, and its second connection comes after the first refusal's line is stuck.
func TestARefusalIsAnsweredWhileTheLogCannotBeWritten(t *testing.T) {
	for name, row := range map[string]struct {
		waitingBound, refusingBound, connections int
	}{
		// Every request is past the waiting bound, refused on a goroutine of its own.
		"past the waiting bound": {0, maxRefusing, 1},
		// A request decided in a slot and refused there.
		"refused in a decision slot": {maxWaiting, maxRefusing, 1},
		// A second refusal, while the first one's line is the one stuck on the log.
		"past the waiting bound, after a refusal whose line is stuck": {0, 1, 2},
	} {
		t.Run(name, func(t *testing.T) {
			withWaitingBound(t, row.waitingBound)
			withRefusingBound(t, row.refusingBound)
			keys := testnats.NewCalloutKeys(t)
			url := testnats.StartWithCallout(t, keys)
			log := startWithStalledLog(t, url, keys, Deps{})
			log.stalled.Store(true)
			for index := range row.connections {
				requireRefusedAtOnce(t, url, []string{"first", "second"}[index])
			}
		})
	}
}

// A refusal whose log line cannot be written does not keep its decision slot from the callers
// behind it. When answer wrote the refusal's line itself, a request refused in a slot still held
// the slot while that line waited (deliver releases it only once answer returns), so while the log
// was stalled every refusal made in a slot took that slot out of the replica for good, and once the
// slots were gone no caller, however live its credential, was decided. With one slot: a connection
// holding no credential is refused in it, and a live session connecting after it must be admitted
// inside half a second.
func TestARefusalStuckOnTheLogHoldsNoDecisionSlot(t *testing.T) {
	withDecisionSlots(t, 1)
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	live := newSession(t, "ses_live", 15*time.Minute)
	log := startWithStalledLog(t, url, keys, Deps{Introspect: introspector(live)})
	log.stalled.Store(true)

	requireRefusedAtOnce(t, url, "first")
	began := time.Now()
	nc, err := connectSession(t, url, live, nil)
	took := time.Since(began)
	require.NoError(t, err, "the live session was not admitted (after %s): the only slot was held by a refusal stuck on the log", took)
	nc.Close()
	require.Less(t, took, 500*time.Millisecond, "the live session was admitted after %s, behind a refusal stuck on the log", took)
}
