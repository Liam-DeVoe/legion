package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
)

// A relay whose every probe read reads a false "down" - a box loaded past the probe's budget -
// backs off its restarts: each restart's first session-consumer call answers ErrConnectionLost at
// once (connForRequest), the relay waits in Reconnected, which reads nats.go connected and returns
// at once, and a second loss in a row within relayRetryFirst of its restart waits the retry backoff
// (relay). Its context is the relay's whole run, so no call budget bounds it, unlike a registration
// or a delete. The row counts the passes in 3 s of a seam that reads "down" for every read,
// spending the probe's budget on each.
//
// The control is the relay restarting at once on every loss: about 30 passes in the 3 s.
func TestARelayWhoseProbeReadsDownBacksOffItsRestarts(t *testing.T) {
	rig := newRelayRig(t)
	const sessionID = "ses_relay_false_down"
	standIn := startSessionStandIn(t, rig.url, sessionID, answerDone)
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1"))
	before, _ := rig.storeEvent(t, contracts.LegacyAgentSubject(sessionID))
	requireFrames(t, standIn.collect(t, 1, 5*time.Second), before)

	const line = "session relay lost its NATS connection"
	restore := rig.deps.consumers.consumers.ProbeReadsDownForTest(func() bool {
		time.Sleep(probeBudget)
		return true
	})
	defer restore()
	start := strings.Count(rig.harness.logs.String(), line)
	rig.deps.consumers.consumers.CloseConnection()
	const window = 3 * time.Second
	time.Sleep(window)
	passes := strings.Count(rig.harness.logs.String(), line) - start
	restore()
	t.Logf("restart passes that lost the connection in %s: %d", window, passes)
	if passes > 5 {
		t.Fatalf("the relay lost its connection %d times in %s of a false \"down\": its restarts never back off while the probe reads down, want at most 5 (the retry backoff from %s)", passes, window, relayRetryFirst)
	}
	if passes == 0 {
		t.Fatal("the relay never lost its connection: the row shows nothing of a restart loop")
	}
}
