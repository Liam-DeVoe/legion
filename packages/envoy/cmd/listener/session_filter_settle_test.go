package main

// What one attempt of a fenced filter write could not settle, the settle loop attempts again
// after a pause, under the session's lock for each attempt alone, until the consumer filters on
// the row the bucket holds or the session is gone (settleAgain, settleLoop). The cases here make
// the bus refuse the writes of an unsubscribe's refollow for a while and then answer them, and
// hold the filter to the row the bucket holds within one pause of the bus answering; stop the
// loop by deleting the session's record while the bus still refuses; and move the row under every
// pass of a confirm's attempt, which ends out of passes and leaves the newest row to the loop.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/store"
)

// settleRefusals is how many of the settle's writes the bus refuses in the cases below before
// it answers: as many as one attempt makes passes, so a fence that stopped at its own passes or
// at its first refused write would leave the filter standing.
const settleRefusals = filterFenceAttempts

// refusingBus makes every update of sessionID's consumer on rig's session-consumer connection
// fail, unsent, with a JetStream API error - the server's own answer that it changed nothing -
// until stopRefusing is called, and counts the updates it refused and when the last one was.
type refusingBus struct {
	mu       sync.Mutex
	refused  int
	last     time.Time
	refusing bool
}

func refuseUpdates(t *testing.T, rig *sessionConsumerRig, sessionID string) *refusingBus {
	t.Helper()
	refusals := &refusingBus{refusing: true}
	name, _ := bus.SessionConsumerName(sessionID)
	rig.deps.consumers.consumers.SetUpdateFault(func(consumer string) error {
		refusals.mu.Lock()
		defer refusals.mu.Unlock()
		if consumer != name || !refusals.refusing {
			return nil
		}
		refusals.refused++
		refusals.last = time.Now()
		return &jetstream.APIError{Code: 503, ErrorCode: 10077, Description: "the test's bus refuses this update"}
	})
	t.Cleanup(func() { rig.deps.consumers.consumers.SetUpdateFault(nil) })
	return refusals
}

// count is how many updates the bus has refused, and when it refused the last.
func (b *refusingBus) count() (int, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.refused, b.last
}

// stopRefusing makes the bus answer every update from now on, and answers when that was.
func (b *refusingBus) stopRefusing() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refusing = false
	return time.Now()
}

// awaitRefusals waits, for at most within, until the bus has refused n updates, and fails the
// test naming how many it did refuse when it has not.
func (b *refusingBus) awaitRefusals(t *testing.T, n int, within time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		refused, _ := b.count()
		if refused >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the bus refused %d of the settle's writes in %v and no more, want %d: the listener stopped writing the filter", what, refused, within, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An unsubscribe whose refollow the bus refuses leaves the consumer filtering on the topic it
// removed (refollow). The settle loop writes it again after each refusal, one pause apart, and
// once the bus answers the consumer filters on the row the bucket holds within one pause and one
// attempt's own budget: the pause is what the next attempt waits before it starts
// (filterSettlePause), and the budget bounds the attempt the bus answers (fenceBudget). Each
// attempt the loop starts again is a WARN line naming the session and why.
func TestASettleTheBusRefusesKeepsTryingUntilTheFilterIsTheRowTheBucketHolds(t *testing.T) {
	rig := newSessionConsumerRig(t)
	const sessionID = "ses_settle_refused"
	const kept = "notifications.dispatch.issue.SETTLEREFUSEDKEPT.>"
	const dropped = "notifications.dispatch.issue.SETTLEREFUSEDDROP.>"
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept, dropped))

	refusing := refuseUpdates(t, rig, sessionID)
	rig.post(t, "/v1/interests/unsubscribe", `{"session_id":"`+sessionID+`","topics":["`+dropped+`"]}`)
	// The refollow's own write and the loop's attempts after it, one pause apart.
	refusing.awaitRefusals(t, settleRefusals, time.Duration(settleRefusals)*(filterSettlePause+sessionConsumerCallTimeout), "while the bus refused")
	answering := refusing.stopRefusing()

	bound := filterSettlePause + sessionConsumerCallTimeout
	deadline := answering.Add(bound)
	for !rig.filtersAre(t, sessionID, kept) {
		if time.Now().After(deadline) {
			refused, _ := refusing.count()
			t.Fatalf("the consumer of %s filters on %v %v after the bus answered again, want the row the bucket holds (%s) within %v: the bus refused %d writes and the settle wrote no more", sessionID, rig.filters(t, sessionID), time.Since(answering).Round(time.Millisecond), kept, bound, refused)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("the consumer filtered on the row the bucket holds %v after the bus answered again", time.Since(answering).Round(time.Millisecond))
	awaitSettled(rig)
	requireFilters(t, rig, sessionID, kept)

	// Each attempt the loop started again names the session and why, and the last one says it
	// settled the filter.
	again := settleLines(t, rig, sessionID, settleAgainLine)
	if len(again) < settleRefusals-1 {
		t.Errorf("the settle loop logged %d attempts started again, want at least %d, one for each write the bus refused after the refollow's own", len(again), settleRefusals-1)
	}
	for _, line := range again {
		if line.Why != fenceNotWritten.String() || line.Pause != filterSettlePause.String() {
			t.Errorf("an attempt started again names why %q and pause %q, want %q and %q: the attempt before it wrote nothing", line.Why, line.Pause, fenceNotWritten.String(), filterSettlePause.String())
		}
	}
	if len(settleLines(t, rig, sessionID, settleSettledLine)) != 1 {
		t.Errorf("the settle loop logged no line saying it settled the filter of %s:\n%s", sessionID, rig.harness.logs.String())
	}
	rig.store(t, "notifications.dispatch.issue.SETTLEREFUSEDKEPT.comment")
	rig.awaitPending(t, sessionID, 1)
}

// The control of the case above: a session that is gone stops the settle loop, which says so by
// name. The bus refuses every write here, and the session's record is deleted while the loop
// pauses, as a deregistration or a reaper pass deletes it: the loop's next attempt reads the
// record, finds it gone, writes nothing and stops (settleGoneLine), and the bus is asked nothing
// more.
func TestASettleOfASessionThatIsGoneStopsTryingByName(t *testing.T) {
	rig := newSessionConsumerRig(t)
	const sessionID = "ses_settle_gone"
	const kept = "notifications.dispatch.issue.SETTLEGONEKEPT.>"
	const dropped = "notifications.dispatch.issue.SETTLEGONEDROP.>"
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept, dropped))

	refusing := refuseUpdates(t, rig, sessionID)
	rig.post(t, "/v1/interests/unsubscribe", `{"session_id":"`+sessionID+`","topics":["`+dropped+`"]}`)
	refusing.awaitRefusals(t, 2, 2*(filterSettlePause+sessionConsumerCallTimeout), "before the record's delete")
	if err := rig.deps.sessions.Delete(context.Background(), sessionID); err != nil {
		t.Fatalf("delete the record of %s: %v", sessionID, err)
	}
	// The loop's next attempt, a pause on, finds the record gone and says so. A loop that went on
	// would make an attempt each pause and never say it, and the case fails here rather than wait.
	deadline := time.Now().Add(3 * (filterSettlePause + sessionConsumerCallTimeout))
	for len(settleLines(t, rig, sessionID, settleGoneLine)) == 0 {
		if time.Now().After(deadline) {
			refused, _ := refusing.count()
			t.Fatalf("the settle loop of %s did not stop for the session being gone: the bus was asked %d writes, and no line names it gone:\n%s", sessionID, refused, rig.harness.logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	awaitSettled(rig)
	refused, _ := refusing.count()
	if gone := settleLines(t, rig, sessionID, settleGoneLine); len(gone) != 1 {
		t.Fatalf("the settle loop of %s logged %d lines saying the session is gone, want 1:\n%s", sessionID, len(gone), rig.harness.logs.String())
	}
	// Nothing more: the loop has returned (awaitSettled), and two pauses pass with no write.
	time.Sleep(2 * filterSettlePause)
	if after, _ := refusing.count(); after != refused {
		t.Errorf("the bus was asked %d more writes after the loop stopped for the session being gone", after-refused)
	}
	// The consumer the refollow could not write is left as it was, filtering on the topic the
	// unsubscribe removed: no session reads it, and the record's delete is what takes it.
	requireFilters(t, rig, sessionID, kept, dropped)
}

// A fence out of passes names the session and both revisions at WARN, and the settle loop then
// writes the filter of the newest row, with no heartbeat behind it. The hook below stores a row in
// each pass of the confirm's own attempt, after the pass has read the row it plans from
// (moveRowUnder), so every write that attempt makes is outrun and it ends holding the row of its
// last plan, one row behind the bucket: that window is what the line reports. The row then stops
// moving, and the loop's attempt after its pause plans from the newest row and ends on it
// (fenceFilterWrite, settleAgain). The hook stays in place until the loop has run, so no write of
// it races the loop's read of it, and moves the row in the confirm's own passes alone.
func TestAFenceOutOfPassesNamesBothRevisionsAndTheSettleLoopWritesTheNewestRow(t *testing.T) {
	a := newSessionConsumerRig(t)
	const sessionID = "ses_fence_exhausted"
	const kept = "notifications.dispatch.issue.FENCEOUTKEPT.>"
	a.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept))

	topics, passes := []string{kept}, 0
	a.deps.consumers.afterRepairPlan = func() {
		passes++
		if passes > filterFenceAttempts {
			return
		}
		topic := fmt.Sprintf("notifications.dispatch.issue.FENCEOUT%d.>", passes)
		topics = append(topics, topic)
		moveRowUnder(t, a, sessionID, topic)
	}
	a.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept))
	wrote, newest := requireFenceExhaustionLogged(t, a, sessionID)
	awaitSettled(a)
	a.deps.consumers.afterRepairPlan = nil

	if passes <= filterFenceAttempts {
		t.Fatalf("the repair and the settle loop made %d passes, want the %d of the confirm's attempt and the loop's after them", passes, filterFenceAttempts)
	}
	if newest <= wrote {
		t.Errorf("the line names revision %d written and %d newest, want a newest above it", wrote, newest)
	}
	again := settleLines(t, a, sessionID, settleAgainLine)
	if len(again) != 1 || again[0].Why != fenceOutrun.String() || again[0].After != repairAfterRegistration {
		t.Errorf("the settle loop started %d attempts again (%+v), want one, after the registration's repair that was left on an older row", len(again), again)
	}
	// The newest row, with no heartbeat behind it: the settle loop wrote it.
	requireFilters(t, a, sessionID, topics...)
	a.store(t, "notifications.dispatch.issue.FENCEOUT"+strconv.Itoa(filterFenceAttempts)+".comment")
	a.awaitPending(t, sessionID, 1)
}

// moveRowUnder adds topic to sessionID's interest row with one bucket write and sets the
// session's consumer filter to the row it stored with one consumer call, as a registration
// answered on another listener does, and fails the test when either fails. A hook of a repair's
// pass calls it once the pass has read the row it plans from (afterRepairPlan), so the pass's
// write carries the row this one replaced, and changes the consumer: a pass whose write found
// the consumer as its plan asks would send nothing and end the fence (filterWriteSettled).
func moveRowUnder(t *testing.T, rig *sessionConsumerRig, sessionID, topic string) {
	t.Helper()
	row, err := rig.harness.registry.Upsert(store.Interest{SessionID: sessionID, MachineID: "test-machine"}, []string{topic})
	if err != nil {
		t.Errorf("store %s under the pass's plan: %v", topic, err)
		return
	}
	if err := rig.deps.consumers.consumers.UpdateFilter(context.Background(), sessionID, rig.deps.consumers.sessionConsumerFilter(sessionID, row.Topics)); err != nil {
		t.Errorf("set the consumer of %s to the row stored under the pass's plan: %v", sessionID, err)
	}
}

// settleLine is what the settle loop's lines name (settleAgainLine, settleSettledLine,
// settleGoneLine, settleStoppingLine).
type settleLine struct {
	SessionID string `json:"session_id"`
	After     string `json:"after"`
	Attempt   int    `json:"attempt"`
	Attempts  int    `json:"attempts"`
	Why       string `json:"why"`
	Pause     string `json:"pause"`
}

// settleLines answers every line msg the listener logged for sessionID, in order.
func settleLines(t *testing.T, rig *sessionConsumerRig, sessionID, msg string) []settleLine {
	t.Helper()
	var lines []settleLine
	for line := range strings.SplitSeq(rig.harness.logs.String(), "\n") {
		if !strings.Contains(line, `"msg":"`+msg+`"`) {
			continue
		}
		var logged settleLine
		if err := json.Unmarshal([]byte(line), &logged); err != nil {
			t.Fatalf("decode the listener's line %s: %v", line, err)
		}
		if logged.SessionID == sessionID {
			lines = append(lines, logged)
		}
	}
	return lines
}

// filtersAre reports whether sessionID's consumer filters on its agent subject and topics, as
// requireFilters requires, without failing the test.
func (rig *sessionConsumerRig) filtersAre(t *testing.T, sessionID string, topics ...string) bool {
	t.Helper()
	return slices.Equal(rig.filters(t, sessionID), bus.SessionConsumerFilters(append([]string{contracts.LegacyAgentSubject(sessionID)}, topics...)))
}

// stopSettling stops the work rig's listener left behind its answers and waits for it
// (awaitAfterAnswer), as the listener's stop does: a case whose settle loop can never settle -
// its bucket reads made impossible - or that takes away a hook such a loop could still read ends
// that work first, and fails when it does not end.
func stopSettling(t *testing.T, rig *sessionConsumerRig) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*consumerFilterFenceWait)
	defer cancel()
	rig.deps.consumers.awaitAfterAnswer(ctx)
	if ctx.Err() != nil {
		t.Fatalf("the work the listener left behind its answers was still running %v after its stop", 3*consumerFilterFenceWait)
	}
}
