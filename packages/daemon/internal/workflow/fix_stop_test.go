package workflow

import (
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// stop is the owner's stop of LEGION-208's task a.
func (r *fixRig) stop() {
	r.t.Helper()
	if result := r.apply("stop", ownersStop(r.t, issueOf(r.t, r.pool, r.engine, "LEGION-208"), "a")); result.Refusal != nil {
		r.t.Fatalf("the owner's stop was refused: %+v", result.Refusal)
	}
}

// todo is a person's todo on LEGION-208, which starts its stopped task again at its first step.
func (r *fixRig) todo() {
	r.t.Helper()
	if result := r.apply("todo", personsTodo("LEGION-208")); result.Refusal != nil {
		r.t.Fatalf("the person's todo was refused: %+v", result.Refusal)
	}
}

// stepStops is the role of each step_stop the outbox holds for LEGION-208, oldest first.
func (r *fixRig) stepStops() []string {
	r.t.Helper()
	var roles []string
	for _, stop := range superviseRowsOf(r.t, r.pool, "LEGION-208", "step_stop") {
		roles = append(roles, string(stop.Role))
	}
	return roles
}

// assertWayDropped fails unless the latest fix of LEGION-208's task a has ended with result at
// endHead and takes no way (Way 4).
func (r *fixRig) assertWayDropped(result, endHead string) {
	r.t.Helper()
	if fix := r.latestFix(); fix.State != record.FixEnded || fix.Result != result || fix.EndHead != endHead || fix.Way != 4 {
		r.t.Fatalf("the fix = %+v, want it ended %s at %s, taking no way", fix, result, endHead)
	}
}

// A stop while the review runs beside the fix ends both: the fix ends stopped and takes no way,
// the runs of the review and of the fix end stopped, the step of each gets its step_stop, and no
// step starts. The control, kept: the stop of a task with no fix sends the one step_stop of its
// running step, as the base does.
func TestAStopEndsTheFixAndTheReview(t *testing.T) {
	r := startedFix(t, intake.FixJobRead{Name: "unit", URL: fixPage})

	r.stop()

	r.assertWayDropped("stopped", fixHead)
	r.assertRunEnded(3, 1, ResultStopped, 0)
	r.assertRunEnded(1, 2, ResultStopped, 0)
	stops := r.stepStops()
	slices.Sort(stops)
	assertStrings(t, "the step_stops", stops, []string{string(claim.RoleImplementer), string(claim.RoleReviewer)})
	issue := issueOf(t, r.pool, r.engine, "LEGION-208")
	if issue.Hold == nil || *issue.Hold != (record.Hold{From: phase.Reviewing, Reason: record.HoldTaskStopped}) || issue.RunningFix != 0 {
		t.Fatalf("LEGION-208 after the stop stands in %s with hold %+v and running fix %d, want held from reviewing for the stopped task, no fix",
			issue.Phase, issue.Hold, issue.RunningFix)
	}

	t.Run("a task with no fix gets one step_stop", func(t *testing.T) {
		r := reviewing(t)
		r.stop()
		assertStrings(t, "the step_stops", r.stepStops(), []string{string(claim.RoleReviewer)})
	})
}

// A restart after a stop beside a review that a fix ran beside moves on: the fix pushed a commit
// and was stopped, and the restarted implementer's end makes its row's move, to the tester, which
// starts once more.
func TestARestartAfterAStopBesideAReviewMovesOn(t *testing.T) {
	r := startedFix(t, intake.FixJobRead{Name: "unit", URL: fixPage})
	r.pushed(headTwo)
	r.stop()
	r.todo()

	r.move(TriggerImplementationReady, "", record.PhaseRow{})

	assertPhase(t, r.pool, phase.Testing)
	if got := stepStartCounts(t, r.pool, "LEGION-208"); !slices.Contains(got, "a/step/2=2") {
		t.Fatalf("the starts of LEGION-208 = %v, want a/step/2=2: the tester starts once more", got)
	}
	r.assertWayDropped("stopped", headTwo)
}

// A stop after the fix ended beside a review, its way pending, takes no way: the fix keeps its
// result and its end, and takes way 4; after a todo the restarted implementer's end moves to the
// tester, and no return is counted.
func TestAStopAfterTheFixEndedBesideAReviewTakesNoWay(t *testing.T) {
	r := startedFix(t, intake.FixJobRead{Name: "unit", URL: fixPage})
	r.pushed(headTwo)
	r.apply("fix-done", fixCompletion(headTwo))
	r.stop()
	r.todo()

	r.move(TriggerImplementationReady, "", record.PhaseRow{})

	r.assertWayDropped("passed", headTwo)
	assertPhase(t, r.pool, phase.Testing)
	if counts := r.counts(); len(counts.BackwardMoves) != 0 {
		t.Fatalf("the task counts %+v, want no return", counts)
	}
}

// A stop drops a return the ended fix stored: the fix failed back to the planner while the review
// ran, its way pending; after the stop and a todo the task walks on from its first step, and no
// return is counted for the fix's report.
func TestAStopDropsAStoredReturnToThePlanner(t *testing.T) {
	r := planningFix(t)
	r.apply("fix-back", fixMoveBack(phase.Planning))
	r.stop()
	r.todo()

	r.move(TriggerPlannerCompleted, "", record.PhaseRow{})
	r.move(TriggerImplementationReady, "", record.PhaseRow{})

	if fix := r.latestFix(); fix.Way != 4 || fix.ReturnTo != 1 || fix.Result != "failed" {
		t.Fatalf("the fix = %+v, want it failed back to step 1, taking no way", fix)
	}
	assertPhase(t, r.pool, phase.Testing)
	if counts := r.counts(); len(counts.BackwardMoves) != 0 {
		t.Fatalf("the task counts %+v, want no return", counts)
	}
}

// A stop while the fix reads its job ends the fix stopped, taking no way; the read's answer then
// changes nothing and starts no implementer; after a todo, a failure at a new head starts a new
// fix, which no open fix refuses.
func TestAStopWhileTheFixReadsItsJob(t *testing.T) {
	r := reviewing(t)
	r.fails(fixHead, "k-ci")
	r.stop()

	r.assertWayDropped("stopped", fixHead)
	r.readJob(intake.FixJobRead{Name: "unit", URL: fixPage})
	if starts := r.fixStarts(); len(starts) != 0 {
		t.Fatalf("the starts of a fix after the stopped fix's read = %+v, want none", starts)
	}

	r.todo()
	r.move(TriggerImplementationReady, "", record.PhaseRow{})
	r.move(TriggerTesterPassed, "", record.PhaseRow{})
	r.pushed(headTwo)
	r.fails(headTwo, "k-ci")

	if fix := r.latestFix(); fix.Fix != 2 || fix.State != record.FixReading || fix.Head != headTwo {
		t.Fatalf("the latest fix = %+v, want fix 2 reading its job at %s", fix, headTwo)
	}
}

// An issue that leaves the workflow ends its fix: the fix ends stopped and takes no way.
func TestAnIssueThatLeavesEndsItsFix(t *testing.T) {
	r := startedFix(t, intake.FixJobRead{Name: "unit", URL: fixPage})
	parked := personsTodo("LEGION-208")
	parked.Status = "backlog"

	if result := r.apply("parked", parked); result.Refusal != nil {
		t.Fatalf("the person's move to backlog was refused: %+v", result.Refusal)
	}

	r.assertWayDropped("stopped", fixHead)
}
