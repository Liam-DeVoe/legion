package supervise

// How a stop waits for the copy a turn's end started (R1 of dispatch://INFRA-1117), beyond the
// plain case of turncopy_test.go: a stop that reaches an idle role while its turn's copy is still
// being taken, a claim in a turn again when the copy ends, a later turn's end while the stop
// waits, a compaction's end, and a hold dropped while it waits.

import (
	"errors"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// holdStop asks the daemon's suspension of the claim, and fails unless the machine holds it.
func holdStop(t *testing.T, h *harness, reason string) {
	t.Helper()
	if err := h.handle(RequestSuspend{Claim: h.token, Reason: reason, By: SuspendedByDaemon}); !errors.Is(err, ErrSuspendHeld) {
		t.Fatalf("the daemon's suspension (%s) = %v, want it held", reason, err)
	}
}

// A pause stops an idle role at once, and a role whose turn ended a moment before is idle while
// the copy of that turn's end is still asking its shim: stopped then, the copy fails and the end of
// the turn is in no copy once the role is stopped. So a stop that reaches a ready or idle claim
// while the copy of its last turn's end is still being taken is held and waits for that copy, as a
// stop held for the turn's end does, though nothing held it then. The claim stays idle on its
// process meanwhile, the stop transaction is asked nothing, and the copy's end runs the pause's
// stop through it.
//
// The control is that hold taken out: the pause's stop then runs at once, while the copy is still
// being taken, and the test fails at the stop's answer.
func TestAPausesStopOfARoleWhoseTurnsCopyIsStillBeingTakenWaitsForThatCopy(t *testing.T) {
	copies := &turnEndCopies{}
	h, flag, stops := pauseHarness(t, queuedClaim(), func(d *Deps) { d.CopyTurnEnd = copies.start })
	h.reach(StateIdle)
	if started := copies.started(); len(started) != 1 {
		t.Fatalf("the turn's end started %d copies, want one", len(started))
	}
	loc := h.locator()
	flag.set(7)

	if err := h.handle(pauseStop(h.token, 7)); !errors.Is(err, ErrSuspendHeld) {
		t.Fatalf("the pause's stop of an idle role whose turn's copy is still being taken = %v, want it held for that copy", err)
	}
	h.wantState(StateIdle)
	h.wantCalls("Suspend", 0)
	stops.wantStops(t)
	if c := h.claim(); !c.SuspensionHeld || c.Locator == nil || *c.Locator != loc {
		t.Fatalf("the claim waiting for its turn's copy is %+v, want its process kept and the pause's stop held", c)
	}

	h.must(copies.copied(t, 0))

	h.wantState(StateSuspended)
	if suspend := h.wantCalls("Suspend", 1)[0]; suspend.Locator != loc {
		t.Fatalf("suspended %+v, want the process whose turn ended, %+v", suspend.Locator, loc)
	}
	stops.wantStops(t, stopCall{stop: PauseStop{Pause: 7}, landed: true})
}

// A stop that reaches an idle claim while its turn's copy is being taken waits for it as a stop
// held for the turn's end waits: no longer than the copy's budget from the turn's end, and never
// past the stop's own deadline, which for an operator's suspension is the stop timeout from its
// arrival. A suspension that may not be held, the one a fresh session posts, waits for no copy:
// its asker read the claim in no turn and is owed the stop at once. Nor does one asked once the
// copy's budget has run out with the copy still unreported: a held stop's wait for the copy ends
// at its due (turnCopyHolds), and a suspension asked after it is decided at once, as where no copy
// is taken, and is not answered held.
//
// The control is the hold taken out, as above: the daemon's and the operator's stops then run at
// once, and those cases fail at the stop's answer. The row past the due holds no control of its
// own: with the hold asked of any copy still being taken, whatever its due, no wait for the copy
// begins and suspend runs the held stop in the same decision, so the row ends the same either
// way. That control fails TestASuspensionPastTheCopysDueOverAPausesHeldStopIsDecidedByItsAsker
// instead (turncopy_past_due_test.go).
func TestAStopThatReachesAnIdleClaimWaitsForItsTurnsCopyNoLongerThanEitherBound(t *testing.T) {
	for _, tc := range []struct {
		name string
		ask  RequestSuspend
		// after is how long after the turn's end the stop is asked, and lands how long after it is
		// asked it stops the process: zero for a stop decided at once.
		after, lands time.Duration
	}{
		{name: "a daemon's suspension goes at the copy's budget from the turn's end",
			ask: RequestSuspend{By: SuspendedByDaemon, Reason: "the tree of LEGION-208 lingers"}, after: time.Minute, lands: testCopyBudget - time.Minute},
		{name: "an operator's suspension goes at its stop timeout",
			ask: RequestSuspend{By: SuspendedByOperator}, lands: testStop},
		{name: "a fresh session's suspension goes at once",
			ask: RequestSuspend{By: SuspendedByOperator, NeverHold: true, Reason: "a fresh session was asked for"}},
		{name: "a daemon's suspension past the copy's budget goes at once",
			ask: RequestSuspend{By: SuspendedByDaemon, Reason: "the tree of LEGION-208 lingers"}, after: testCopyBudget + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := copyingHarness(t)
			h.reach(StateIdle)
			h.advance(tc.after)
			turnCopyDue(t, h)
			tc.ask.Claim = h.token

			err := h.handle(tc.ask)

			if tc.lands == 0 {
				if err != nil {
					t.Fatalf("the suspension = %v, want it decided now", err)
				}
				h.wantState(StateSuspended)
				h.wantCalls("Suspend", 1)
				return
			}
			if !errors.Is(err, ErrSuspendHeld) {
				t.Fatalf("the suspension of an idle claim whose turn's copy is being taken = %v, want it held", err)
			}
			h.advance(tc.lands - time.Second)
			h.wantState(StateIdle)
			h.wantCalls("Suspend", 0)
			h.advance(time.Second)
			h.wantState(StateSuspended)
			h.wantCalls("Suspend", 1)
		})
	}
}

// A claim in a turn again when the copy its held stop waited for ends keeps its process: the agent
// started a turn of its own while the stop waited, and a stop now would cut that turn off. The
// held stop runs at that turn's end instead, once the copy that end starts has ended, as a stop
// held for a turn always runs.
//
// The control is the copy's end running the held stop whatever the claim is doing: the process is
// then stopped in the middle of the agent's own turn, and the test fails at the stop.
func TestAHeldStopWhoseClaimIsInATurnAgainWhenItsCopyEndsWaitsForThatTurnsEnd(t *testing.T) {
	h, copies := copyingHarness(t)
	h.reach(StateWorking)
	holdStop(t, h, "LEGION-208 left implementing")
	h.must(StreamTurnEnd{Claim: h.token})
	h.wantState(StateIdle)

	h.must(StreamTurnStart{Claim: h.token})
	h.wantState(StateWorking)
	h.must(copies.copied(t, 0))

	h.wantState(StateWorking)
	h.wantCalls("Suspend", 0)
	if c := h.claim(); !c.SuspensionHeld {
		t.Fatalf("the claim in its own turn holds %+v, want the stop still held for that turn's end", c)
	}

	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	if started := copies.started(); len(started) != 2 {
		t.Fatalf("the turns' ends started %d copies, want one each", len(started))
	}
	h.wantState(StateIdle)
	h.wantCalls("Suspend", 0)

	h.must(copies.copied(t, 1))

	h.wantState(StateSuspended)
	h.wantCalls("Suspend", 1)
}

// A turn that ends while a held stop waits for the copy of an earlier turn's end — one the daemon
// never saw begin, here — moves the wait onto its own copy: that copy holds the later end, and the
// daemon takes it behind the earlier one. So the earlier copy's end runs nothing, nor does the
// earlier copy's budget, and the stop runs at the later copy's end.
//
// The control is the later turn's copy leaving the stop's wait behind: the later copy's end then
// runs nothing either, and the test fails at its stop.
func TestAHeldStopWaitingForATurnsCopyWaitsForTheCopyOfALaterTurnInstead(t *testing.T) {
	h, copies := copyingHarness(t)
	h.reach(StateWorking)
	holdStop(t, h, "LEGION-208 left implementing")
	h.must(StreamTurnEnd{Claim: h.token})
	h.advance(2 * time.Minute)

	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	if started := copies.started(); len(started) != 2 {
		t.Fatalf("the turns' ends started %d copies, want one each", len(started))
	}

	h.must(copies.copied(t, 0))
	h.wantState(StateIdle)
	h.wantCalls("Suspend", 0)
	h.advance(testCopyBudget - 2*time.Minute)
	h.wantState(StateIdle)
	h.wantCalls("Suspend", 0)

	h.must(copies.copied(t, 1))

	h.wantState(StateSuspended)
	h.wantCalls("Suspend", 1)
}

// A stop held under a compaction runs at the compaction's end, and waits there first for the copy
// a turn's end started that is still being taken: the turn that ended under this compaction above
// all, whose copy is asking the process the stop would take. The compaction answers that the claim
// was not given back, the claim is idle on its process meanwhile, holding the stop, and the copy's
// end runs it.
//
// The control is the compaction's end running the held stop with no wait: the process is then
// stopped while the copy is still being taken, and the test fails at the stop.
func TestACompactionsEndUnderAHeldStopWaitsForTheCopyOfTheTurnThatEndedUnderIt(t *testing.T) {
	h, copies := copyingHarness(t)
	h.reach(StateIdle)
	h.must(copies.copied(t, 0))
	release, compacted := h.compactHeld()
	h.must(StreamTurnStart{Claim: h.token})
	holdStop(t, h, "LEGION-208 left implementing")
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	if started := copies.started(); len(started) != 2 {
		t.Fatalf("the turns' ends started %d copies, want one each", len(started))
	}
	h.wantState(StateWorking)

	release()

	if err := <-compacted; !errors.Is(err, ErrCompactSuspended) {
		t.Fatalf("the compaction whose end a held stop took = %v, want ErrCompactSuspended", err)
	}
	h.m.Wait()
	h.wantState(StateIdle)
	h.wantCalls("Suspend", 0)
	if c := h.claim(); !c.SuspensionHeld || c.Compacting {
		t.Fatalf("the claim after its compaction holds %+v, want the stop held and the compaction over", c)
	}

	h.must(copies.copied(t, 1))

	h.wantState(StateSuspended)
	h.wantCalls("Suspend", 1)
}

// A held stop dropped while it waits for its turn's copy takes its wait with it: a start run
// against the claim drops a daemon's hold, the copy goes on, and its end runs nothing. A
// suspension that reaches the claim after the drop, while the copy is still being taken, waits for
// that copy afresh and no longer than the copy's budget from the turn's end, as any stop that
// reaches a claim whose turn's copy is being taken does.
//
// The control is the drop leaving the wait's mark on the copy: the later suspension then reads as
// joining a stop that already waits, arms no bound, and a copy that never ends holds it past its
// budget, so the second case fails at the stop.
func TestADroppedStopTakesItsWaitForTheTurnsCopyWithIt(t *testing.T) {
	dropped := func(t *testing.T) (*harness, *turnEndCopies) {
		t.Helper()
		h, copies := copyingHarness(t)
		h.reach(StateWorking)
		holdStop(t, h, "LEGION-208 left implementing")
		h.must(StreamTurnEnd{Claim: h.token})
		if err := h.m.StartedBy(h.ctx, 42, nil); err != nil {
			t.Fatalf("record the start: %v", err)
		}
		h.m.Wait()
		if c := h.claim(); c.SuspensionHeld || c.State != StateIdle {
			t.Fatalf("the claim after the start is %s (held %v), want idle holding no stop", c.State, c.SuspensionHeld)
		}
		return h, copies
	}

	t.Run("the copy's end runs nothing", func(t *testing.T) {
		h, copies := dropped(t)
		h.must(copies.copied(t, 0))
		h.advance(testCopyBudget)
		h.wantState(StateIdle)
		h.wantCalls("Suspend", 0)
	})

	t.Run("a suspension after the drop waits for the copy afresh, no longer than its budget", func(t *testing.T) {
		h, _ := dropped(t)
		h.advance(time.Minute)
		holdStop(t, h, "the tree of LEGION-208 lingers")
		h.advance(testCopyBudget - time.Minute - time.Second)
		h.wantState(StateIdle)
		h.wantCalls("Suspend", 0)
		h.advance(time.Second)
		h.wantState(StateSuspended)
		h.wantCalls("Suspend", 1)
	})
}

// dueAtTheGate is h's clock with one seam: the read suspend's gate makes (turnCopyInBudget, asked
// by suspend) answers the time as it stands, and then moves the clock to due, firing nothing, so
// the copy's due falls between that read and the next one of the same decision.
type dueAtTheGate struct {
	*fakeClock
	due   time.Time
	moved bool
}

func (c *dueAtTheGate) Now() time.Time {
	gate := readBySuspendsGate()
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now
	if gate && !c.moved {
		c.moved, c.now = true, c.due
	}
	return now
}

// readBySuspendsGate is whether the clock is being read by turnCopyInBudget asked by suspend
// itself, the gate's read, rather than through waitForTurnCopy or anything else.
func readBySuspendsGate() bool {
	pc := make([]uintptr, 32)
	frames := goruntime.CallersFrames(pc[:goruntime.Callers(2, pc)])
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.Function, ".(*Machine).turnCopyInBudget") {
			asker, _ := frames.Next()
			return strings.HasSuffix(asker.Function, "/supervise.suspend")
		}
		if !more {
			return false
		}
	}
}

// gateMoved is whether the gate's read came and moved the clock to the due.
func (c *dueAtTheGate) gateMoved() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.moved
}

// suspend reads the clock in its gate (turnCopyInBudget) and again where the wait for the copy
// begins (waitForTurnCopy). A suspension that reaches an idle claim while its turn's copy is being
// taken, with the copy's due falling between those two reads, is held at the gate, and then no
// wait for the copy begins: no bound is armed, and the copy's end runs only a stop whose wait has
// begun (endTurnCopyWait). The held stop runs in that decision instead, as it would have past the
// due, and the claim is suspended by the asker, who is answered that nothing is held.
//
// The control is suspend without that run: the claim is then left idle holding the stop with no
// wait for the copy, which nothing runs, and the test fails there.
func TestASuspensionWhoseCopysDueFallsInsideItsDecisionRunsInThatDecision(t *testing.T) {
	h, _ := copyingHarness(t)
	h.reach(StateIdle)
	h.advance(time.Minute)
	clock := &dueAtTheGate{fakeClock: h.clock, due: turnCopyDue(t, h)}
	h.m.mu.Lock()
	h.m.deps.Clock = clock
	h.m.mu.Unlock()

	err := h.handle(RequestSuspend{Claim: h.token, By: SuspendedByDaemon, Reason: "the tree of LEGION-208 lingers"})

	if !clock.gateMoved() {
		t.Fatal("suspend's gate never read the clock, so the copy's due did not fall inside the decision")
	}
	if c := h.claim(); c.SuspensionHeld && !h.armed(TimerTurnCopy) {
		t.Fatalf("the claim is %s holding the stop with no wait for the copy (the suspension answered %v): nothing will run it", c.State, err)
	}
	if err != nil {
		t.Fatalf("the suspension whose copy's due fell inside its decision = %v, want it decided in that decision", err)
	}
	h.wantCalls("Suspend", 1)
	if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != SuspendedByDaemon || c.SuspensionHeld {
		t.Fatalf("the claim is %s by %q holding a stop = %t, want it suspended by the daemon, holding none", c.State, c.SuspendedBy, c.SuspensionHeld)
	}
}
