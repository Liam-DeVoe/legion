package main

// Every attempt of a fenced filter write runs on a budget of its own, sessionConsumerCallTimeout
// from when it starts under the session's lock, which neither the act before it nor its caller's
// deadline shortens, while its caller's cancellation still ends it (fenceBudget). The cases here
// drive that budget through the acts that start a fence - a relay restart, a consumer delete, an
// unsubscribe - with the act's own budget spent first (afterOwnCalls), with the caller cancelled
// after or before that, and on a bus slow enough that the attempt outlives its budget; and hold
// fenceBudget itself to a caller that is cancelled already.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// spendOwnBudget is an afterOwnCalls hook that spends the act's whole budget
// (sessionConsumerCallTimeout) once its own calls have answered, as a restart or a delete on a
// bus slow to answer them spends it.
func spendOwnBudget() { time.Sleep(sessionConsumerCallTimeout + 100*time.Millisecond) }

// The corrections after a relay restart run on the fence's own budget, not on what the restart's
// read and call left of the restart's (fenceBudget). Here a registration on another listener
// stores a newer row under the restart's read, the restart's own write of the older row lands
// over it, and the restart's calls then spend its whole budget: the restart still settles the
// consumer on the newest row before it pulls. A restart whose corrections are bounded by what its
// own calls left, or that skips them once that is spent, leaves the relay pulling a consumer
// filtered on the older row, so the topic that registration was answered 200 for is skipped.
func TestARelayRestartWhoseCallsSpentItsBudgetStillSettlesTheFilterOnTheNewestRow(t *testing.T) {
	a := newRelayRigUnowned(t, relayPauseFirst, relayPauseMax)
	b := newSessionConsumerRig(t)
	const sessionID = "ses_restart_spent"
	const kept = "notifications.dispatch.issue.RESTARTSPENTKEPT.>"
	const added = "notifications.dispatch.issue.RESTARTSPENTADD.>"
	a.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept))

	var stored sync.Once
	a.deps.consumers.afterStoredRowRead = func() {
		stored.Do(func() { b.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept, added)) })
	}
	a.deps.consumers.afterOwnCalls = spendOwnBudget
	ctx, cancel := context.WithTimeout(context.Background(), 3*consumerFilterFenceWait)
	defer cancel()
	_, pull, _ := a.relays.restart(ctx, &relayRun{sessionID: sessionID})
	a.deps.consumers.afterStoredRowRead, a.deps.consumers.afterOwnCalls = nil, nil
	if pull != nil {
		pull.Stop()
	}

	requireFilters(t, b, sessionID, kept, added)
	b.store(t, "notifications.dispatch.issue.RESTARTSPENTADD.comment")
	b.awaitPending(t, sessionID, 1)
}

// The control of the case above: the fence's budget is its own, and its caller's cancellation
// still ends it, since it means the caller itself is ending - a relay whose run was cancelled, a
// loop the shutdown stopped (fenceBudget). The same settle, handed a context cancelled rather than
// one whose deadline passed, makes no pass, and the restart's filter of the older row stands.
func TestARelayRestartWhoseRunWasCancelledSettlesNothing(t *testing.T) {
	a := newRelayRigUnowned(t, relayPauseFirst, relayPauseMax)
	b := newSessionConsumerRig(t)
	const sessionID = "ses_restart_cancelled"
	const kept = "notifications.dispatch.issue.RESTARTCANCELLEDKEPT.>"
	const added = "notifications.dispatch.issue.RESTARTCANCELLEDADD.>"
	a.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept))
	_, wrote, err := a.harness.registry.RefreshAt(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("read the row the restart writes from: %v", err)
	}
	b.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept, added))

	unlock := a.deps.consumers.locks.lock(sessionID)
	if err := a.deps.consumers.consumers.UpdateFilter(context.Background(), sessionID, a.deps.consumers.sessionConsumerFilter(sessionID, []string{kept})); err != nil {
		unlock()
		t.Fatalf("write the restart's filter of the older row: %v", err)
	}
	ended, end := context.WithCancel(context.Background())
	end()
	a.relays.settleFilter(ended, sessionID, wrote)
	unlock()

	requireFilters(t, b, sessionID, kept)
}

// A relay restart's corrections end with the relay's run whenever it is cancelled - after the
// restart's own budget has passed as well as before it - since the restart hands them the run's
// own context and not its calls' timed child: a child whose deadline has passed answers that
// deadline from then on, whatever its parent does, and a fence handed one would go on after a
// relay stop, into the grace the stop has (sessionRelays.stop). Here every correction is slow and
// outrun again, and the run is cancelled while one is in flight: that one may finish, and no
// pass begins after it.
func TestARelayRestartCancelledAfterItsOwnBudgetEndsItsCorrections(t *testing.T) {
	requireRestartCancelledDuringItsCorrectionsEndsThem(t, "ses_restart_cancel_late", true)
}

// The control of the case above: the run cancelled before the restart's own budget has passed.
func TestARelayRestartCancelledBeforeItsOwnBudgetEndsItsCorrections(t *testing.T) {
	requireRestartCancelledDuringItsCorrectionsEndsThem(t, "ses_restart_cancel_early", false)
}

// requireRestartCancelledDuringItsCorrectionsEndsThem drives a relay restart whose corrections
// are each slow and outrun again by a registration on another listener, with the restart's own
// budget spent first when late, cancels the run 200 ms into the corrections, and fails the test
// when a correction begins after that cancellation.
func requireRestartCancelledDuringItsCorrectionsEndsThem(t *testing.T, sessionID string, late bool) {
	t.Helper()
	a := newRelayRigUnowned(t, relayPauseFirst, relayPauseMax)
	b := newSessionConsumerRig(t)
	const kept = "notifications.dispatch.issue.RESTARTCANCELKEPT.>"
	a.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept))

	var reads atomic.Int32
	a.deps.consumers.afterStoredRowRead = func() {
		n := reads.Add(1)
		if n > 1 {
			// Each correction is slow, and a registration on another listener outruns it.
			time.Sleep(400 * time.Millisecond)
		}
		b.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept, fmt.Sprintf("notifications.dispatch.issue.RESTARTCANCEL%d.>", n)))
	}
	run, stop := context.WithCancel(context.Background())
	defer stop()
	var mu sync.Mutex
	var readsAtCancel int32
	var cancelled time.Time
	a.deps.consumers.afterOwnCalls = func() {
		if late {
			spendOwnBudget()
		}
		go func() {
			time.Sleep(200 * time.Millisecond)
			mu.Lock()
			readsAtCancel, cancelled = reads.Load(), time.Now()
			mu.Unlock()
			stop()
		}()
	}
	_, pull, _ := a.relays.restart(run, &relayRun{sessionID: sessionID})
	returned := time.Now()
	a.deps.consumers.afterStoredRowRead, a.deps.consumers.afterOwnCalls = nil, nil
	if pull != nil {
		pull.Stop()
	}
	mu.Lock()
	defer mu.Unlock()
	if cancelled.IsZero() {
		t.Fatalf("the restart returned before its run was cancelled, after %d reads of the row: the corrections this case is about did not run", reads.Load())
	}
	begun := reads.Load() - readsAtCancel
	t.Logf("late=%t: the restart returned %v after its run was cancelled; %d reads of the row in all, %d begun after the cancellation", late, returned.Sub(cancelled).Round(time.Millisecond), reads.Load(), begun)
	if begun > 0 {
		t.Fatalf("the restart's corrections began %d passes and ran %v after the relay's run was cancelled", begun, returned.Sub(cancelled).Round(time.Millisecond))
	}
}

// The repair after a consumer delete runs on the fence's own budget, not on what the delete's
// read and call left of the delete's (deleteConsumer, fenceBudget). Here a registration on another
// listener writes the session's record back between the delete's read and its call, which is
// the window the repair is for, and the delete's own calls then spend its whole budget: the
// repair still gives that session its consumer back, on the row the bucket holds. A repair
// bounded by what the delete left, or skipped once that is spent, leaves the session, answered
// 200 with consumer delivery, with no consumer until its next heartbeat.
func TestARepairWhoseDeleteSpentItsBudgetStillGivesTheConsumerBack(t *testing.T) {
	rig := newSessionConsumerRig(t)
	const sessionID = "ses_delete_spent"
	const kept = "notifications.dispatch.issue.DELETESPENTKEPT.>"
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept))

	ctx, cancel := context.WithTimeout(context.Background(), 3*consumerFilterFenceWait)
	defer cancel()
	// The record delete every delete of a consumer follows, with the record read first so the
	// registration that races the delete can write it back.
	record, err := rig.harness.sessions.Refresh(ctx, sessionID)
	if err != nil {
		t.Fatalf("read the record of %s: %v", sessionID, err)
	}
	if err := rig.harness.sessions.Delete(ctx, sessionID); err != nil {
		t.Fatalf("delete the record of %s the delete below follows: %v", sessionID, err)
	}
	rig.deps.consumers.afterOwnCalls = func() {
		if err := rig.harness.sessions.Put(sessionID, record); err != nil {
			t.Errorf("the entry write that raced the delete of %s: %v", sessionID, err)
		}
		spendOwnBudget()
	}
	if !rig.deps.consumers.deleteConsumer(ctx, rig.deps, sessionID, nil) {
		t.Fatalf("the delete of %s's consumer reported it standing; want it gone, the bucket holding no record when the delete read it", sessionID)
	}
	rig.deps.consumers.afterOwnCalls = nil

	requireFilters(t, rig, sessionID, kept)
	rig.store(t, "notifications.dispatch.issue.DELETESPENTKEPT.comment")
	rig.awaitPending(t, sessionID, 1)
}

// One attempt of a fenced filter write holds the session's lock for its own budget and the pass
// in flight when that budget ends, and no longer, however slow the bus and however often the row
// moves (fenceBudget): the unsubscribe below is answered within sessionConsumerCallTimeout and
// one pass, and the settle loop writes what that attempt left (settleAgain). Each pass here reads
// a row another listener then replaces, and is slow; with no budget of its own the attempt would
// make all of its passes under the lock, at three seconds each, which is the bound the body's
// waits rest on: what waits behind that lock - the session's registrations, a restart, a delete -
// would wait for all of them.
func TestAFenceAttemptOnASlowBusHoldsTheSessionsLockNoLongerThanItsBudget(t *testing.T) {
	a := newSessionConsumerRig(t)
	b := newSessionConsumerRig(t)
	const sessionID = "ses_fence_slow_bus"
	const kept = "notifications.dispatch.issue.FENCESLOWKEPT.>"
	const dropped = "notifications.dispatch.issue.FENCESLOWDROP.>"
	a.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept, dropped))

	const pass = 3 * time.Second
	var reads atomic.Int32
	a.deps.consumers.afterStoredRowRead = func() {
		n := reads.Add(1)
		time.Sleep(pass)
		b.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept, fmt.Sprintf("notifications.dispatch.issue.FENCESLOW%d.>", n)))
	}
	began := time.Now()
	a.post(t, "/v1/interests/unsubscribe", `{"session_id":"`+sessionID+`","topics":["`+dropped+`"]}`)
	held := time.Since(began)
	bound := sessionConsumerCallTimeout + pass + 2*time.Second
	t.Logf("the unsubscribe's refollow held the session's lock %v over %d passes", held.Round(time.Millisecond), reads.Load())
	if held > bound {
		t.Fatalf("the unsubscribe's refollow held the session's lock %v over %d passes, past its budget (%v) and the pass in flight at its end (%v): an attempt with no budget of its own makes every pass under the lock", held.Round(time.Millisecond), reads.Load(), sessionConsumerCallTimeout, pass)
	}
	awaitSettled(a)
	a.deps.consumers.afterStoredRowRead = nil
	requireFilteredOnStoredRow(t, a, sessionID)
}

// A fence's budget whose caller is cancelled already is cancelled already, before any wait: the
// fence's first read sees it rather than racing the callback that would cancel it
// (fenceBudget). Each round below reads the budget the moment it is made; a budget cancelled only
// by that callback is not yet in most of them. The control: a caller whose deadline has passed
// gets a budget that has a deadline of its own to come.
func TestAFenceBudgetOfACallerCancelledAlreadyIsCancelledAtOnce(t *testing.T) {
	for round := range 100 {
		caller, cancel := context.WithCancel(context.Background())
		cancel()
		budget, stop := fenceBudget(caller)
		err := budget.Err()
		stop()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("round %d: the budget of a caller cancelled already answers %v at once, want context.Canceled", round, err)
		}
	}
	spent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	budget, stop := fenceBudget(spent)
	defer stop()
	if err := budget.Err(); err != nil {
		t.Fatalf("the budget of a caller whose deadline passed answers %v, want a budget of its own", err)
	}
	if deadline, ok := budget.Deadline(); !ok || time.Until(deadline) < sessionConsumerCallTimeout-time.Second {
		t.Fatalf("the budget of a caller whose deadline passed ends at %v (%t), want sessionConsumerCallTimeout from now", deadline, ok)
	}
}
