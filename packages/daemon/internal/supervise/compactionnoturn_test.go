package supervise

// A compaction with no turn started under it keeps the claim busy and runs no turn (busy,
// inTurn). These tests pin the reads that tell the two apart there: what a suspension that may not
// be held is told, what the held suspension's deadline takes back, and what the stale-plugin
// relaunch of the controller's claim waits for.

import (
	"errors"
	"strings"
	"testing"
)

// A suspension that may not be held (RequestSuspend.NeverHold) that meets a compaction with no
// turn under it is refused, records nothing, and is told that the claim's session is being
// compacted: no turn runs for it to have waited for (holdSuspension, reportWaitNow). The
// compaction then gives the claim back as it would have.
//
// The negative control is the refusal's reason read off the stored state alone
// (`m.claim.State != StateWorking` before this change): it says the agent is in a turn.
func TestASuspensionThatIsNeverHeldIsToldOfTheCompactionWithNoTurnUnderIt(t *testing.T) {
	h, release, compacted := compactingIdle(t)

	err := h.handle(RequestSuspend{Claim: h.token, Reason: "a fresh session was asked for",
		By: SuspendedByOperator, NeverHold: true})

	var refused *RefusedError
	if !errors.As(err, &refused) || refused.State != StateWorking {
		t.Fatalf("the suspension returned %v, want a refusal naming %s", err, StateWorking)
	}
	if !strings.Contains(refused.Reason, "being compacted") || strings.Contains(refused.Reason, "in a turn") {
		t.Fatalf("the refusal says %q, want it to name the compaction and no turn", refused.Reason)
	}
	if c := h.claim(); c.SuspensionHeld {
		t.Fatalf("the claim reports %+v, want nothing held for the compaction's end", c)
	}
	release()
	if err := <-compacted; err != nil {
		t.Fatalf("the compaction answered %v, want the claim given back", err)
	}
	h.m.Wait()
	h.wantState(StateIdle)
	h.wantCalls("Suspend", 0)
}

// A held suspension whose deadline comes while a compaction with no turn under it runs, after the
// take's turn ended with its report still to come, cuts off no turn: the take is where the wait
// for its report says (reportWaitNow), so the wait's one take-back takes the task back with the
// note of a cut turn, by the hold's deadline (reportMissed), and the stop lands. The resumed
// process takes the task once, behind that note.
//
// The negative control is the deadline read off the stored state (`m.claim.State ==
// StateWorking` before this change): it logs a turn stopped mid-turn, and the take-back names the
// turn the deadline ended rather than the report that did not come.
func TestAHeldDeadlineDuringACompactionAfterTheTakesTurnIsTheReportsBound(t *testing.T) {
	h, number, stops := reportToComeOfACurrentPlugin(t)
	release, compacted := h.compactHeld()
	holdsTheSuspension(h)

	h.advance(testStop)

	h.wantState(StateSuspended)
	h.wantCalls("Suspend", stops+1)
	h.wantTakenBackWithItsNote(number)
	if lines := h.logs.lines(reportMissedLine, "the held suspension's deadline"); len(lines) != 1 {
		t.Fatalf("logged %q, want the report's take-back by the hold's deadline once", lines)
	}
	if lines := h.logs.lines("suspending it mid-turn"); len(lines) != 0 {
		t.Fatalf("logged %q, want no turn stopped mid-turn", lines)
	}
	release()
	if err := <-compacted; !errors.Is(err, ErrCompactProcessGone) {
		t.Fatalf("the compaction whose process the hold stopped answered %v, want ErrCompactProcessGone", err)
	}
	h.m.Wait()
	h.wantTakenOnceMore(number, resumedByTheOperator)
}

// The stale-plugin relaunch of the controller's claim waits out no turn and no compaction
// (refreshPlugin), but it waits for the report of its take's turn as every claim's does: over a
// compaction with no turn under it, begun after that turn ended with its report still to come,
// the relaunch waits, the compaction's end leaves it waiting for the report (afterTurn), and the
// report finishes the task once before the relaunch runs. The relaunched process takes nothing of
// the task.
//
// The negative control is the report's wait read only where the claim is not stored working
// (`m.claim.State != StateWorking && m.reportHolds()` before this change): the relaunch runs
// mid-compaction, takes the task back as a cut turn, and the relaunched process runs again a task
// whose turn ran whole.
func TestTheControllersStaleRelaunchDuringACompactionWaitsForTheReport(t *testing.T) {
	h := newHarnessOf(t, controllerClaim(), currentPlugin)
	h.reach(StateReady)
	h.takesTurnEndsWithItsReportToCome()
	number, stops := h.pending().Number, len(h.calls("Suspend"))
	release, compacted := h.compactHeld()
	h.deps.PluginContract, h.m.deps.PluginContract = TaskTakeContract+1, TaskTakeContract+1

	h.must(StalePlugin{Claim: h.token, Trigger: "sweep"})

	h.wantState(StateWorking)
	h.wantCalls("Resume", 0)
	h.wantTakesTurnStanding(number, "under the compaction,")
	release()
	if err := <-compacted; err != nil {
		t.Fatalf("the compaction answered %v, want the claim given back", err)
	}
	h.m.Wait()
	h.wantStillAwaitingItsReport(number, stops)

	h.mustReport(nil)

	h.wantState(StateLaunching)
	h.wantCalls("Resume", 1)
	h.wantFinishedOnce(finishedByReport)
	h.wantTakenNoMore(number, relaunchedToReady)
}
