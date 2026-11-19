package main

// Rows for the relay's backoff of a quick second loss (sessionRelays.relay).

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
)

const relayLostLine = "session relay lost its NATS connection"

// A relay whose every probe read reads a false "down" backs off its restarts even when each
// restart waits more than relayRetryFirst for the session's lock (or its restart turn) before it
// asks the probe: the time the restart waited in line is no part of how quickly it was lost.
// Here the session's lock is taken by another holder for 1.1 s each time the relay releases it,
// as other relays' restarts and registrations hold the turns and the lock on a loaded listener.
// A loss is timed from when the restart took the session's lock (relayRun.lockedAt), so the wait
// in line does not hide a quick loss.
//
// The control is the loss timed from before the restart's wait in line: no loss reads "quick", and
// the relay restarts with no backoff, about one restart per lock hold.
func TestARelayWhoseRestartWaitsInLineStillBacksOff(t *testing.T) {
	rig := newRelayRig(t)
	const sessionID = "ses_relay_wait_in_line"
	standIn := startSessionStandIn(t, rig.url, sessionID, answerDone)
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1"))
	before, _ := rig.storeEvent(t, contracts.LegacyAgentSubject(sessionID))
	requireFrames(t, standIn.collect(t, 1, 5*time.Second), before)

	locks := &rig.deps.consumers.locks
	var grabbing atomic.Bool
	restore := rig.deps.consumers.consumers.ProbeReadsDownForTest(func() bool {
		// The relay holds the session's lock while it asks the probe: the other holder queues
		// for the lock now and takes it the moment the relay lets it go. One such holder at a
		// time, so each restart waits one hold.
		if grabbing.CompareAndSwap(false, true) {
			go func() {
				unlock := locks.lock(sessionID)
				time.Sleep(1100 * time.Millisecond)
				unlock()
				grabbing.Store(false)
			}()
		}
		time.Sleep(probeBudget)
		return true
	})
	defer restore()
	start := strings.Count(rig.harness.logs.String(), relayLostLine)
	rig.deps.consumers.consumers.CloseConnection()
	const window = 20 * time.Second
	time.Sleep(window)
	passes := strings.Count(rig.harness.logs.String(), relayLostLine) - start
	restore()
	t.Logf("restart passes that lost the connection in %s: %d", window, passes)
	if passes > 8 {
		t.Fatalf("the relay lost its connection %d times in %s of a false \"down\" while each restart waited 1.1 s in line: no loss read quick, so it never backed off, want at most 8", passes, window)
	}
	if passes < 2 {
		t.Fatalf("the relay lost its connection %d times: the row shows no restart loop", passes)
	}
}

// A run that served the session for at least relayRetryMax resets the relay's retry backoff to
// relayRetryFirst, so a relay that met a false "down" for a few seconds once does not carry the
// doubled backoff for the rest of its life: the next real drop followed by one false "down" holds
// the session's messages for about relayRetryFirst, not for the backoff the earlier episode grew.
//
// The control is the relay without that reset: the frame stored at the drop arrives after about
// 16 s.
func TestARelayBackoffAfterAHealthyRunStartsAgainFromTheFirst(t *testing.T) {
	rig := newRelayRig(t)
	const sessionID = "ses_relay_backoff_reset"
	standIn := startSessionStandIn(t, rig.url, sessionID, answerDone)
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1"))
	first, _ := rig.storeEvent(t, contracts.LegacyAgentSubject(sessionID))
	requireFrames(t, standIn.collect(t, 1, 5*time.Second), first)

	// Episode one: 8 s of a false "down" on every probe read.
	restore := rig.deps.consumers.consumers.ProbeReadsDownForTest(func() bool {
		time.Sleep(probeBudget)
		return true
	})
	start := strings.Count(rig.harness.logs.String(), relayLostLine)
	rig.deps.consumers.consumers.CloseConnection()
	time.Sleep(8 * time.Second)
	restore()
	t.Logf("episode one: %d lost restarts", strings.Count(rig.harness.logs.String(), relayLostLine)-start)
	// The session's registration ends the relay's wait; the relay serves it again.
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1"))
	second, _ := rig.storeEvent(t, contracts.LegacyAgentSubject(sessionID))
	awaitRelayedFrame(t, standIn, second, 10*time.Second)

	// A healthy run longer than relayRetryMax.
	time.Sleep(relayRetryMax + 2*time.Second)

	// Episode two: a real drop, then one false "down" on the restart that follows.
	var reads atomic.Int64
	restore = rig.deps.consumers.consumers.ProbeReadsDownForTest(func() bool {
		if reads.Add(1) == 1 {
			time.Sleep(probeBudget)
			return true
		}
		return false
	})
	defer restore()
	rig.deps.consumers.consumers.CloseConnection()
	stored := time.Now()
	third, _ := rig.storeEvent(t, contracts.LegacyAgentSubject(sessionID))
	took := awaitRelayedFrame(t, standIn, third, 30*time.Second).arrived.Sub(stored)
	t.Logf("episode two: the frame stored at the drop arrived after %s", took)
	if took > 4*time.Second {
		t.Fatalf("after a healthy run of %s, a drop and one false \"down\" held the session's frame %s: the relay's retry backoff, doubled by the losses of an episode long past, was never put back to %s", relayRetryMax+2*time.Second, took, relayRetryFirst)
	}
}

// awaitRelayedFrame waits for the frame carrying want, passing over the redelivery of an earlier frame
// whose ack a close took (a restart at the ack floor sends it again).
func awaitRelayedFrame(t *testing.T, s *sessionStandIn, want contracts.Envelope, within time.Duration) relayedFrame {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case frame := <-s.frames:
			if frame.item.EventID == want.EventID {
				return frame
			}
		case <-deadline:
			t.Fatalf("no frame carrying %s within %s", want.EventID, within)
		}
	}
}
