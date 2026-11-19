package main

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/session"
)

// probeBudget is the probe's budget (bus's heldLockBudget): the seam below spends it on each read it
// makes read "down", as a read nats.go's lock keeps waiting does on a loaded box, so a caller that
// retries on each "down" retries no faster than it would there.
const probeBudget = 100 * time.Millisecond

// requireNoErrorLog fails the test when the rig's listener logged at ERROR.
func requireNoErrorLog(t *testing.T, rig *sweepRig) {
	t.Helper()
	if logs := rig.harness.logs.String(); strings.Contains(logs, `"level":"ERROR"`) || strings.Contains(logs, "level=ERROR") {
		t.Fatalf("the listener logged at ERROR: %s", logs)
	}
}

// registration is the body of a registration of sessionID that asks for its session consumer.
func registration(sessionID string) string {
	return `{"session_id":"` + sessionID + `","self_subscribed":true,"topics":[],"delivery":{"consumer":true,"epoch":"e"}}`
}

// sessionLockTaken reports whether anybody holds or waits for sessionID's lock.
func sessionLockTaken(l *sessionLocks, sessionID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.locks[sessionID] != nil
}

// A registration whose consumer call made under the session's lock reads a false "down" from the
// probe - the read a loaded box keeps waiting past the probe's budget, made here by the seam for
// the first read made while the lock is taken - is answered 200 with its consumer: the call answers
// the loss at once, the registration releases the session's lock, waits for the connection with
// nothing held (relockAfterReconnect, Reconnected, which reads nats.go itself), takes the lock again
// and makes the call again, which lands. Nothing is logged at ERROR. The seam reads "down" only
// while the lock is taken, so the false "down" lands on the call under it, not on the stream read
// made before it.
//
// The control is the registration's locked call not retried after a loss (the loop on
// relockAfterReconnect taken out): the registration is refused with 503.
func TestARegistrationWhoseLockedCallReadsAFalseDownRelocksAndLands(t *testing.T) {
	rig := newSweepRig(t)
	const sessionID = "ses_false_down_locked"
	var lockedDowns, reads atomic.Int64
	locks := &rig.deps.consumers.locks
	restore := rig.deps.consumers.consumers.ProbeReadsDownForTest(func() bool {
		reads.Add(1)
		if sessionLockTaken(locks, sessionID) && lockedDowns.Add(1) == 1 {
			time.Sleep(probeBudget)
			return true
		}
		return false
	})
	defer restore()
	rig.register(t, sessionID)
	if lockedDowns.Load() < 2 {
		t.Fatalf("the probe was read %d times under the session's lock (%d reads in all), want a read made again after the false down under the lock", lockedDowns.Load(), reads.Load())
	}
	rig.requireConsumer(t, sessionID)
	requireNoErrorLog(t, rig)
}

// A registration of a session that has a consumer, whose stream read answers and whose every later
// probe read in its budget reads "down" - the false "down" landing on the create made under the
// session's lock, for the whole of the registration's budget (sessionConsumerCallTimeout) - ends
// with the usual refusal, 503 consumer_error, as a registration during a real outage does, logs
// nothing at ERROR, and does not hold the session back: its next registration, made once the probe
// answers again, is answered.
//
// The controls: the refusal answered 500 fails it; the refusal logged at ERROR fails it; a lost
// answer starting the no-grant backoff fails the next registration.
func TestARegistrationWhoseCreateReadsDownForItsWholeBudgetAnswers503(t *testing.T) {
	rig := newSweepRig(t)
	const sessionID = "ses_false_down_create_budget"
	rig.register(t, sessionID)
	var reads atomic.Int64
	restore := rig.deps.consumers.consumers.ProbeReadsDownForTest(func() bool {
		if reads.Add(1) >= 3 {
			time.Sleep(probeBudget)
			return true
		}
		return false
	})
	refusal := rig.refusedSubscribe(t, registration(sessionID))
	restore()
	t.Logf("probe reads: %d; answered %d %q", reads.Load(), refusal.status, refusal.Reason)
	if refusal.status != http.StatusServiceUnavailable || refusal.Reason != deliveryReasonConsumerError {
		t.Fatalf("the registration whose create read down for its whole budget answered %d %q (%s), want 503 %q", refusal.status, refusal.Reason, refusal.raw, deliveryReasonConsumerError)
	}
	requireNoErrorLog(t, rig)
	rig.register(t, sessionID)
}

// A delete of a gone session's consumer whose every probe read in its budget reads "down" reports
// the consumer kept, as a delete during a real outage does, and logs nothing at ERROR; the sweep's
// next pass, made once the probe answers again, deletes it.
//
// The controls: the delete reporting the consumer gone on a lost call fails it; the delete's
// failure logged at ERROR fails it.
func TestADeleteWhoseProbeReadsDownForItsWholeBudgetLeavesTheConsumerToTheSweep(t *testing.T) {
	rig := newSweepRig(t)
	const sessionID = "ses_false_down_delete"
	rig.register(t, sessionID)
	rig.now.advance(sweepLapse)
	rig.requireState(t, sessionID, session.Gone)
	restore := rig.deps.consumers.consumers.ProbeReadsDownForTest(func() bool {
		time.Sleep(probeBudget)
		return true
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*sessionConsumerCallTimeout)
	defer cancel()
	gone := rig.deps.consumers.deleteConsumer(ctx, rig.deps, sessionID, nil)
	restore()
	if gone {
		t.Fatal("the delete whose probe read down for its whole budget reported the consumer gone")
	}
	requireNoErrorLog(t, rig)
	rig.sweep.pass(context.Background())
	rig.requireNoConsumer(t, sessionID)
}
