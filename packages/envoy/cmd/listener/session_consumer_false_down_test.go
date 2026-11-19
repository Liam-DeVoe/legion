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

// A registration whose session consumer call reads a false "down" from the probe - the read a loaded
// box keeps waiting past the probe's budget, made here by the seam for the first read alone - is
// answered 200 with its consumer: the call answers the loss at once, the registration releases the
// session's lock, waits for the connection with nothing held (relockAfterReconnect, Reconnected,
// which reads nats.go itself), takes the lock again and makes the call again, which lands. Nothing
// is logged at ERROR.
//
// The control is the row below, where the seam reads "down" for every read: the registration is
// refused.
func TestARegistrationWhoseProbeReadsAFalseDownRetriesAndLands(t *testing.T) {
	rig := newSweepRig(t)
	const sessionID = "ses_false_down_once"
	var reads atomic.Int64
	restore := rig.deps.consumers.consumers.ProbeReadsDownForTest(func() bool {
		if reads.Add(1) == 1 {
			time.Sleep(probeBudget)
			return true
		}
		return false
	})
	defer restore()
	rig.register(t, sessionID)
	if reads.Load() < 2 {
		t.Fatalf("the probe was read %d times, want a read made again after the false down, so the row shows nothing of a retry", reads.Load())
	}
	requireNoErrorLog(t, rig)
}

// A registration whose every probe read in its budget reads "down" - a box loaded past the probe's
// budget for the whole of the registration's (sessionConsumerCallTimeout) - ends with the usual
// refusal, 503 consumer_error, as a registration during a real outage does: the session's existing
// consumer is not dropped, nothing is logged at ERROR, and the session's next registration is
// answered once the probe answers again.
func TestARegistrationWhoseProbeReadsDownForItsWholeBudgetAnswers503AndKeepsTheConsumer(t *testing.T) {
	rig := newSweepRig(t)
	const sessionID = "ses_false_down_budget"
	rig.register(t, sessionID)
	restore := rig.deps.consumers.consumers.ProbeReadsDownForTest(func() bool {
		time.Sleep(probeBudget)
		return true
	})
	refusal := rig.refusedSubscribe(t, registration(sessionID))
	restore()
	if refusal.status != http.StatusServiceUnavailable || refusal.Reason != deliveryReasonConsumerError {
		t.Fatalf("the registration whose probe read down for its whole budget answered %d %q (%s), want 503 %q", refusal.status, refusal.Reason, refusal.raw, deliveryReasonConsumerError)
	}
	rig.requireConsumer(t, sessionID)
	requireNoErrorLog(t, rig)
	rig.register(t, sessionID)
}

// A delete of a gone session's consumer whose every probe read in its budget reads "down" keeps the
// consumer and reports it kept, as a delete during a real outage does, and logs nothing at ERROR;
// the sweep's next pass, made once the probe answers again, deletes it.
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
	rig.requireConsumer(t, sessionID)
	requireNoErrorLog(t, rig)
	rig.sweep.pass(context.Background())
	rig.requireNoConsumer(t, sessionID)
}
