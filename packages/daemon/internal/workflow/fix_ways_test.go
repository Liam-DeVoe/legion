package workflow

import (
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The way a fix takes once it ended and no other step of its task runs (afterFix), row by row of
// the table of a review beside a fix, on `implementer tester reviewer`: the step that starts next,
// the head its working copy moves to, and the returns the task counts. Rows 1 and 2 (an approval
// while the fix runs) end the task's last step, so they are held at the task's end and are tested
// with that hold.

// assertWay fails unless the latest fix of LEGION-208's task took way, the issue stands in at, the
// latest start of role there moves its working copy to moveTo, and the task counts pass returns,
// pair among them where pair is not "".
func (r *fixRig) assertWay(way int, at phase.Phase, role claim.Role, moveTo string, pass int, pair string) {
	r.t.Helper()
	if fix := r.latestFix(); fix.Way != way {
		r.t.Fatalf("the fix took way %d, want %d: %+v", fix.Way, way, fix)
	}
	assertPhase(r.t, r.pool, at)
	starts := r.startsOf(role)
	if len(starts) == 0 || starts[len(starts)-1].Phase != at || starts[len(starts)-1].MoveTo != moveTo {
		r.t.Fatalf("the %s's starts = %+v, want the latest in %s moving its working copy to %q", role, starts, at, moveTo)
	}
	counts := r.counts()
	if counts.Pass != pass || (pair != "" && counts.BackwardMoves[pair] != 1) {
		r.t.Fatalf("the task counts %+v, want %d returns, %q among them", counts, pass, pair)
	}
}

// fixAfterReview is LEGION-208 reviewing on `implementer tester reviewer` with fix 1 for k-ci at
// fixHead: the review's request for changes or approval (trigger) applied while the fix runs, or
// after it ended where afterEnd says so, and the fix ended at end, the head the pull request
// stands at then.
func fixAfterReview(t *testing.T, engine *Engine, trigger TriggerKind, afterEnd bool, end string) *fixRig {
	t.Helper()
	r := newFixRig(t, engine, []string{"implementer", "tester", "reviewer"}, TriggerImplementationReady, TriggerTesterPassed)
	r.fails(fixHead, "k-ci")
	r.readJob(intake.FixJobRead{Name: "unit", URL: fixPage})
	if !afterEnd {
		r.move(trigger, "", reviewReport("the review's report"))
	}
	r.pushed(end)
	if result := r.apply("fix-done", fixCompletion(end)); result.Refusal != nil {
		t.Fatalf("the fix's completion was refused: %+v", result.Refusal)
	}
	if afterEnd {
		r.move(trigger, "", reviewReport("the review's report"))
	}
	return r
}

var (
	reviewingTesting      = phase.Pair(phase.Reviewing, phase.Testing)
	reviewingImplementing = phase.Pair(phase.Reviewing, phase.Implementing)
)

// Row 3: changes while the fix runs, and the fix made new commits: the fix got the report, so the
// steps after it run again at the new head. One return.
func TestRow3ChangesWhileTheFixRunsWithNewCommits(t *testing.T) {
	r := fixAfterReview(t, returnEngine(), TriggerReviewRejected, false, headTwo)
	r.assertWay(2, phase.Testing, claim.RoleTester, headTwo, 1, reviewingTesting)
}

// Row 4: changes while the fix runs, and the fix made no commit: the fix got the report, so the
// steps after it run again at X. One return.
func TestRow4ChangesWhileTheFixRunsWithNoCommit(t *testing.T) {
	r := fixAfterReview(t, returnEngine(), TriggerReviewRejected, false, fixHead)
	r.assertWay(2, phase.Testing, claim.RoleTester, fixHead, 1, reviewingTesting)
}

// Row 5: an approval after the fix ended with new commits: no step starts at X, and once the
// review ended the steps after the fix run again at the new head. One return.
func TestRow5ApprovalAfterTheFixEndedWithNewCommits(t *testing.T) {
	r := fixAfterReview(t, returnEngine(), TriggerReviewApproved, true, headTwo)
	r.assertWay(2, phase.Testing, claim.RoleTester, headTwo, 1, reviewingTesting)
}

// Row 6: changes after the fix ended with new commits: a return of the review's own, to the
// implementation step with the report, at the new head. One return.
func TestRow6ChangesAfterTheFixEndedWithNewCommits(t *testing.T) {
	r := fixAfterReview(t, returnEngine(), TriggerReviewRejected, true, headTwo)
	r.assertWay(1, phase.Implementing, claim.RoleImplementer, headTwo, 1, reviewingImplementing)
	if counts := r.counts(); counts.Rounds != 1 {
		t.Fatalf("the task counts %+v, want the review's one round", counts)
	}
}

// Row 7: an approval after the fix ended with no commit: the review keeps its pass, and the task
// goes on to its end at X. No return.
func TestRow7ApprovalAfterTheFixEndedWithNoCommit(t *testing.T) {
	r := fixAfterReview(t, returnEngine(), TriggerReviewApproved, true, fixHead)
	if fix := r.latestFix(); fix.Way != 3 {
		t.Fatalf("the fix took way %d, want 3: %+v", fix.Way, fix)
	}
	if task := taskOf(t, r.pool, r.engine, "LEGION-208"); task.Place != (record.TaskPlace{Kind: record.PlaceRetro}) {
		t.Fatalf("the task stands at %+v, want its end", task.Place)
	}
	if counts := r.counts(); counts.Pass != 0 {
		t.Fatalf("the task counts %+v, want no return", counts)
	}
}

// Row 8: changes after the fix ended with no commit: a return of the review's own, at X. One
// return.
func TestRow8ChangesAfterTheFixEndedWithNoCommit(t *testing.T) {
	r := fixAfterReview(t, returnEngine(), TriggerReviewRejected, true, fixHead)
	r.assertWay(1, phase.Implementing, claim.RoleImplementer, fixHead, 1, reviewingImplementing)
}

// Rows 3 and 4 on `implementer reviewer`: the step after the fix is the review itself, so the
// reviewer starts again at its own place, a second run of its step, at the new head for row 3 and
// at X for row 4: one return, counted under implementing-reviewing, and the review's one round.
func TestRows3And4ForAnImplementerReviewerTask(t *testing.T) {
	for _, end := range []string{headTwo, fixHead} {
		t.Run("the fix ends at "+end[:4], func(t *testing.T) {
			r := newFixRig(t, returnEngine(), []string{"implementer", "reviewer"}, TriggerImplementationReady)
			r.fails(fixHead, "k-ci")
			r.readJob(intake.FixJobRead{Name: "unit", URL: fixPage})
			r.move(TriggerReviewRejected, "", reviewReport("the review's report"))
			r.pushed(end)
			r.apply("fix-done", fixCompletion(end))

			r.assertWay(2, phase.Reviewing, claim.RoleReviewer, end, 1, phase.Pair(phase.Implementing, phase.Reviewing))
			if starts := r.startsOf(claim.RoleReviewer); len(starts) != 2 {
				t.Fatalf("the reviewer's starts = %+v, want two", starts)
			}
			if got := stepStartCounts(t, r.pool, "LEGION-208"); !slices.Contains(got, "a/step/2=2") {
				t.Fatalf("the starts of LEGION-208 = %v, want a/step/2=2: the review runs a second time", got)
			}
			if counts := r.counts(); counts.Rounds != 1 {
				t.Fatalf("the task counts %+v, want the review's one round", counts)
			}
		})
	}
}

// A tester that passed beside the fix runs again after it: the fix that made new commits ends
// while the review runs, and once the review ended the tester starts a second time, at the new
// head.
func TestATesterThatPassedBesideTheFixRunsAgainAfterIt(t *testing.T) {
	r := newFixRig(t, returnEngine(), []string{"implementer", "tester", "reviewer"}, TriggerImplementationReady)
	r.fails(fixHead, "k-ci")
	r.readJob(intake.FixJobRead{Name: "unit", URL: fixPage})
	r.move(TriggerTesterPassed, "", record.PhaseRow{})
	r.pushed(headTwo)
	r.apply("fix-done", fixCompletion(headTwo))
	r.move(TriggerReviewApproved, "", reviewReport("approved"))

	r.assertWay(2, phase.Testing, claim.RoleTester, headTwo, 1, reviewingTesting)
	if starts := r.startsOf(claim.RoleTester); len(starts) != 2 {
		t.Fatalf("the tester's starts = %+v, want two", starts)
	}
}

// everyReturn is a deployment hook that names no move and fires at every return.
var everyReturn = Hook{Name: "every-return", On: HookEventWorkBack, Every: 1, Roles: []claim.Role{claim.RoleImplementer},
	Prompt: "Say what the task has done so far."}

// hookEngine is returnEngine with everyReturn among its hooks.
func hookEngine() *Engine {
	engine := returnEngine()
	engine.cfg.Hooks = append(engine.cfg.Hooks, everyReturn)
	return engine
}

// firings is how many prompts of hook the outbox holds.
func (r *fixRig) firings(hook string) int {
	r.t.Helper()
	n := 0
	for _, prompt := range hookPrompts(r.t, r.pool) {
		if prompt.Hook == hook {
			n++
		}
	}
	return n
}

// A fix's return fires the deployment's hooks as every return does: a hook with no moves and an
// every of 1 fires on the way-2 return from a hold for the fix. The control, kept: the same hook on
// a way-3 end, which is no return, does not fire.
func TestAHookFiresOnAFixsReturn(t *testing.T) {
	r := fixAfterReview(t, hookEngine(), TriggerReviewRejected, false, headTwo)
	if n := r.firings(everyReturn.Name); n != 1 {
		t.Fatalf("the hook fired %d times on the fix's return, want once", n)
	}

	t.Run("a way-3 end fires nothing", func(t *testing.T) {
		r := fixAfterReview(t, hookEngine(), TriggerReviewApproved, true, fixHead)
		if n := r.firings(everyReturn.Name); n != 0 {
			t.Fatalf("the hook fired %d times on a way-3 end, want never", n)
		}
	})
}

// A way is taken once: rows 3 and 4 and a way-1 fix (row 8), each run one step past its way, count
// no second return. The control, kept: with the way's write taken out (the seam noWayKept), row 3
// returns the task a second time at the tester's next end.
func TestAWayIsTakenOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger TriggerKind
		after   bool
		end     string
		next    TriggerKind
	}{
		{"row 3", TriggerReviewRejected, false, headTwo, TriggerTesterPassed},
		{"row 4", TriggerReviewRejected, false, fixHead, TriggerTesterPassed},
		{"row 8", TriggerReviewRejected, true, fixHead, TriggerImplementationReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fixAfterReview(t, returnEngine(), tc.trigger, tc.after, tc.end)
			way := r.latestFix().Way
			r.move(tc.next, "", record.PhaseRow{})
			if counts := r.counts(); counts.Pass != 1 {
				t.Fatalf("the task counts %+v one step past the way, want one return", counts)
			}
			if fix := r.latestFix(); fix.Way != way {
				t.Fatalf("the fix's way moved from %d to %d one step past it", way, fix.Way)
			}
		})
	}

	t.Run("with the way's write taken out row 3 returns the task a second time", func(t *testing.T) {
		engine := returnEngine()
		engine.noWayKept = true
		r := fixAfterReview(t, engine, TriggerReviewRejected, false, headTwo)
		r.move(TriggerTesterPassed, "", record.PhaseRow{})
		if counts := r.counts(); counts.Pass != 2 {
			t.Fatalf("the task counts %+v with the way unwritten, want a second return", counts)
		}
	})
}
