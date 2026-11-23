package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// fixHead is the head of LEGION-208's pull request #42 (stepRoot), and formerHead a head the pull
// request has left, at which Dispatch still publishes states.
const (
	fixHead    = "head"
	formerHead = "head-before"
)

// fixChecks is the set of checks of LEGION-208 at every head of these tests: k-unit, which no CI
// job runs, and k-ci and k-ci2, which a CI job runs.
var fixChecks = []string{"k-unit", "k-ci", "k-ci2"}

// fixRig is LEGION-208 on the plan its owner gave, or on the project's default steps, whose first
// step started in this release, so its task takes a fix, and every fact applied through engine.
type fixRig struct {
	t      *testing.T
	pool   *pgxpool.Pool
	engine *Engine
	serial int64
}

// newFixRig seeds LEGION-208 at admitted on the plan whose one task's steps are words (none: the
// project's default steps), starts its first step, and moves it by each of moves, as the
// transitions of its line move it: the tests stand past the testing step without deciding it.
func newFixRig(t *testing.T, engine *Engine, words []string, moves ...TriggerKind) *fixRig {
	t.Helper()
	pool := migratedPool(t)
	root := stepRoot(t, pool, phase.Admitted)
	if len(words) > 0 {
		seedStepsPlan(t, pool, root.Key, root.Generation, words...)
	}
	firstStep(t, pool, engine, root.Key)
	for _, trigger := range moves {
		move(t, pool, engine, root.Key, trigger, "", record.PhaseRow{})
	}
	return &fixRig{t: t, pool: pool, engine: engine}
}

// reviewing is LEGION-208 on `implementer tester reviewer`, its reviewer at work.
func reviewing(t *testing.T) *fixRig {
	t.Helper()
	return newFixRig(t, returnEngine(), []string{"implementer", "tester", "reviewer"}, TriggerImplementationReady, TriggerTesterPassed)
}

// fails applies one check.state_changed event at head for each of checks: a check a CI job runs,
// read no for a fail after its job ended with failure, beside fixChecks as the head's set.
func (r *fixRig) fails(head string, checks ...string) {
	r.t.Helper()
	for _, check := range checks {
		r.serial++
		applyStates(r.t, r.pool, r.engine, "LEGION-208", statesRepo, head, r.serial, ofSet(inCI(stateOf(check, "no", "fail", "ci"), "failure"), fixChecks...))
	}
}

// fixes is each fix of LEGION-208's task as `<fix> <state> <check>@<head> place <place>`, by number.
func (r *fixRig) fixes() []string {
	r.t.Helper()
	return outboxStrings(r.t, r.pool, `select fix || ' ' || state || ' ' || check_id || '@' || head || ' place ' || place
		from task_fixes where issue = $1 order by fix`, "LEGION-208")
}

// failures is each failure of a check at a head of LEGION-208's task as `<check>@<head> <went_to>
// <fix>`, by check and head.
func (r *fixRig) failures() []string {
	r.t.Helper()
	return outboxStrings(r.t, r.pool, `select check_id || '@' || head || ' ' || went_to || ' ' || fix
		from ci_failures where issue = $1 order by check_id, head`, "LEGION-208")
}

// jobReads is each read of a fix's failed job the outbox holds for LEGION-208, oldest first.
func (r *fixRig) jobReads() []record.FixJobRead {
	r.t.Helper()
	reads := []record.FixJobRead{}
	for _, row := range issueOutboxOf(r.t, r.pool, "LEGION-208", string(record.OutboxKindFixJobRead)) {
		var read record.FixJobRead
		if err := json.Unmarshal(row.payload, &read); err != nil {
			r.t.Fatalf("decode the job read %s: %v", row.payload, err)
		}
		reads = append(reads, read)
	}
	return reads
}

// told is the body of each role message the outbox holds for role of LEGION-208, oldest first.
func (r *fixRig) told(role claim.Role) []string {
	r.t.Helper()
	return outboxStrings(r.t, r.pool, fmt.Sprintf(`select payload->>'body' from outbox where kind = 'role_message' and issue = $1
		and payload->>'role' = '%s' order by id`, roleToken(r.t, role)), "LEGION-208")
}

// assertStrings fails unless got is want, what naming the list.
func assertStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

// oneJobRead is the read of fix 1's failed job for check at fixHead, of task.
func oneJobRead(task, check string) []record.FixJobRead {
	return []record.FixJobRead{{Generation: 1, Task: task, Fix: 1, Check: check, Head: fixHead}}
}

// toFix is the implementer's role message of a failure of check at fixHead while its own
// implementation step runs.
func toFix(check string) string {
	return "Check " + check + " fails at " + fixHead + ": CI job of the check ended failure. Fix it in this step."
}

// toRunningFix is the implementer's role message of a failure of check at fixHead that joins the
// fix it runs.
func toRunningFix(check string) string {
	return "Check " + check + " fails at " + fixHead + ": CI job of the check ended failure. It is part of your fix."
}

// A check that a CI job runs failing at the head while the review runs starts the task's first
// implementation step again as a fix: the fix reads its failed job first (state reading, at the
// implementer's place), the failure names it, and one read of the job is queued. Nothing is told
// to the implementer yet: the fix's task tells it. The control: with the rule taken out of the
// tail of every fact (the seam noFixRule) the same failure starts nothing.
func TestAFailureWhileTheReviewRunsStartsAFix(t *testing.T) {
	t.Run("the failure starts a fix", func(t *testing.T) {
		r := reviewing(t)
		r.fails(fixHead, "k-ci")
		assertPhase(t, r.pool, phase.Reviewing)
		assertStrings(t, "the fixes", r.fixes(), []string{"1 reading k-ci@head place 1"})
		assertStrings(t, "the failures", r.failures(), []string{"k-ci@head fix 1"})
		if got := r.jobReads(); !reflect.DeepEqual(got, oneJobRead("a", "k-ci")) {
			t.Fatalf("the job reads = %+v, want %+v", got, oneJobRead("a", "k-ci"))
		}
		assertStrings(t, "the implementer's messages", r.told(claim.RoleImplementer), nil)
	})

	t.Run("the control: with the rule taken out the failure starts nothing", func(t *testing.T) {
		r := reviewing(t)
		r.engine.noFixRule = true
		r.fails(fixHead, "k-ci")
		assertStrings(t, "the fixes with no rule", r.fixes(), nil)
		assertStrings(t, "the failures with no rule", r.failures(), nil)
		if got := r.jobReads(); len(got) != 0 {
			t.Fatalf("the job reads with no rule = %+v, want none", got)
		}
	})
}

// A task that takes a fix hears of a failure once, from the fix: the state event starts the fix,
// and the listener's settlement of the same head tells the owner alone, so the implementer gets
// no role message of the failure and the reviewer none either. The control: with the settlement
// told to every role as before (the seam everyRoleToldCI) the implementer gets the settlement's
// message beside the fix, which the subtest asserts.
func TestAFixesCITaskGetsOneDeliveryOfAFailure(t *testing.T) {
	settled := toldBody("CI settled at head: \"build\" succeeded, \"unit\" failed")
	t.Run("the fix alone reaches the implementer", func(t *testing.T) {
		r := reviewing(t)
		r.fails(fixHead, "k-ci")
		applyRefusingNothing(t, r.pool, r.engine, settledAt(fixHead, 1, "unit"))
		assertStrings(t, "the fixes", r.fixes(), []string{"1 reading k-ci@head place 1"})
		assertStrings(t, "the implementer's messages", r.told(claim.RoleImplementer), nil)
		assertStrings(t, "the reviewer's messages", r.told(claim.RoleReviewer), nil)
		assertStrings(t, "the owner's messages", r.told(claim.RoleArchitect), []string{settled})
	})

	t.Run("the control: the settlement told to every role reaches the implementer beside the fix", func(t *testing.T) {
		r := reviewing(t)
		r.engine.everyRoleToldCI = true
		r.fails(fixHead, "k-ci")
		applyRefusingNothing(t, r.pool, r.engine, settledAt(fixHead, 1, "unit"))
		assertStrings(t, "the fixes", r.fixes(), []string{"1 reading k-ci@head place 1"})
		assertStrings(t, "the implementer's messages with every role told", r.told(claim.RoleImplementer), []string{settled})
	})
}

// An issue with no plan its owner gave walks the default steps this release writes at its first
// step, and those take a fix: a failure while its review runs starts one, at the implementer's
// place.
func TestAnIssueOnTheDefaultStepsGetsAFix(t *testing.T) {
	r := newFixRig(t, engineRunningByDefault(t, "implementer", "reviewer"), nil, TriggerImplementationReady)
	assertPhase(t, r.pool, phase.Reviewing)
	r.fails(fixHead, "k-ci")
	assertStrings(t, "the fixes", r.fixes(), []string{"1 reading k-ci@head place 1"})
	if got := r.jobReads(); !reflect.DeepEqual(got, oneJobRead("default", "k-ci")) {
		t.Fatalf("the job reads = %+v, want %+v", got, oneJobRead("default", "k-ci"))
	}
}

// A failure while the task's implementation step runs goes to that step: the failure is the
// implementer's, and the implementer is told once, naming the check, the head and the job's end;
// no fix starts. The same failure in a later event tells nothing more, since its row says the
// implementer was given it.
func TestAFailureWhileTheImplementerRunsGoesToIt(t *testing.T) {
	r := newFixRig(t, returnEngine(), []string{"implementer", "tester", "reviewer"})
	assertPhase(t, r.pool, phase.Implementing)
	r.fails(fixHead, "k-ci")
	assertStrings(t, "the fixes", r.fixes(), nil)
	assertStrings(t, "the failures", r.failures(), []string{"k-ci@head implementer 0"})
	assertStrings(t, "the implementer's messages", r.told(claim.RoleImplementer), []string{toFix("k-ci")})

	r.fails(fixHead, "k-ci")
	assertStrings(t, "the implementer's messages after the same failure again", r.told(claim.RoleImplementer), []string{toFix("k-ci")})
}

// A state at a head the pull request has left starts nothing and tells nobody: Dispatch keeps
// publishing states at a former head, and the fix is of the task's head alone. The control: the
// same failure at the head starts a fix.
func TestAFailureAtAFormerHeadStartsNothing(t *testing.T) {
	r := reviewing(t)
	r.fails(formerHead, "k-ci")
	assertStrings(t, "the fixes after a failure at a former head", r.fixes(), nil)
	assertStrings(t, "the failures after a failure at a former head", r.failures(), nil)
	assertStrings(t, "the implementer's messages", r.told(claim.RoleImplementer), nil)

	r.fails(fixHead, "k-ci")
	assertStrings(t, "the fixes after the failure at the head", r.fixes(), []string{"1 reading k-ci@head place 1"})
}

// A second check failing at the head while the fix reads its job joins that fix, never a second
// one: its failure names fix 1, one job is read, and the implementer is told nothing, since the
// fix's task names each failure it holds. Two failures the daemon holds at once start one fix
// holding both.
func TestASecondFailureAtTheHeadJoinsTheFixThatIsStarting(t *testing.T) {
	t.Run("in a later event", func(t *testing.T) {
		r := reviewing(t)
		r.fails(fixHead, "k-ci")
		r.fails(fixHead, "k-ci2")
		assertStrings(t, "the fixes", r.fixes(), []string{"1 reading k-ci@head place 1"})
		assertStrings(t, "the failures", r.failures(), []string{"k-ci2@head fix 1", "k-ci@head fix 1"})
		if got := r.jobReads(); !reflect.DeepEqual(got, oneJobRead("a", "k-ci")) {
			t.Fatalf("the job reads = %+v, want %+v", got, oneJobRead("a", "k-ci"))
		}
		assertStrings(t, "the implementer's messages", r.told(claim.RoleImplementer), nil)
	})

	t.Run("in one pass", func(t *testing.T) {
		r := reviewing(t)
		r.engine.noFixRule = true
		r.fails(fixHead, "k-ci", "k-ci2")
		r.engine.noFixRule = false
		r.fails(fixHead, "k-ci2")
		assertStrings(t, "the fixes", r.fixes(), []string{"1 reading k-ci@head place 1"})
		assertStrings(t, "the failures", r.failures(), []string{"k-ci2@head fix 1", "k-ci@head fix 1"})
	})
}

// A check failing at the head of a task held for its fix at its end joins the fix that runs:
// one fix holds both failures, the implementer is told the new one is part of its fix, and the
// issue read names the one fix that runs.
func TestASecondFailureAtTheEndJoinsTheRunningFix(t *testing.T) {
	r := reviewing(t)
	r.fails(fixHead, "k-ci")
	move(t, r.pool, r.engine, "LEGION-208", TriggerReviewApproved, "", record.PhaseRow{})
	held := issueOf(t, r.pool, r.engine, "LEGION-208")
	held.Phase, held.Hold = phase.Held, &record.Hold{From: phase.Retro, Reason: record.HoldForFix}
	seedIssue(t, r.pool, held)
	onStored(t, r.pool, r.engine, "LEGION-208", func(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
		fix, err := r.engine.store.RunningFix(ctx, tx, issue.Key, issue.Generation, "a")
		if err != nil || fix == nil {
			return fmt.Errorf("the reading fix = %+v, %v", fix, err)
		}
		at := stepClock
		fix.State, fix.Entry, fix.StartedAt = record.FixRunning, 2, &at
		return r.engine.store.UpdateFix(ctx, tx, *fix)
	})

	r.fails(fixHead, "k-ci2")

	assertStrings(t, "the fixes", r.fixes(), []string{"1 running k-ci@head place 1"})
	assertStrings(t, "the failures", r.failures(), []string{"k-ci2@head fix 1", "k-ci@head fix 1"})
	assertStrings(t, "the implementer's messages", r.told(claim.RoleImplementer), []string{toRunningFix("k-ci2")})
	if issue := issueOf(t, r.pool, r.engine, "LEGION-208"); issue.RunningFix != 1 {
		t.Fatalf("the issue read names running fix %d, want 1", issue.RunningFix)
	}
}

// A failure while the planner runs, before the task's first implementation step, starts no fix
// and writes no failure: the implementation step's start names it.
func TestAFailureWhileThePlannerRunsStartsNoFix(t *testing.T) {
	r := newFixRig(t, returnEngine(), []string{"planner", "implementer", "reviewer"})
	assertPhase(t, r.pool, phase.Planning)
	r.fails(fixHead, "k-ci")
	assertStrings(t, "the fixes", r.fixes(), nil)
	assertStrings(t, "the failures", r.failures(), nil)
	assertStrings(t, "the implementer's messages", r.told(claim.RoleImplementer), nil)
}

// A failure while the task stands at its retro goes to the merger, which reads GitHub: the
// failure names the merger, and nothing else happens.
func TestAFailureInTheRetroGoesToTheMerger(t *testing.T) {
	r := newFixRig(t, returnEngine(), []string{"implementer", "reviewer"}, TriggerImplementationReady, TriggerReviewApproved)
	assertPhase(t, r.pool, phase.Retro)
	r.fails(fixHead, "k-ci")
	assertStrings(t, "the fixes", r.fixes(), nil)
	assertStrings(t, "the failures", r.failures(), []string{"k-ci@head merger 0"})
	assertStrings(t, "the implementer's messages", r.told(claim.RoleImplementer), nil)
	assertStrings(t, "the merger's messages", r.told(claim.RoleMerger), nil)
}

// A task of a run that started before this release takes no fix: a failure while its review runs
// writes no failure and starts no fix, and the listener's settlement tells the owner, the
// reviewer and the implementer, as before.
func TestATaskOfTheReleaseBeforeStartsNoFix(t *testing.T) {
	r := reviewing(t)
	if _, err := r.pool.Exec(t.Context(), `update plan_tasks set fixes_ci = false where issue = 'LEGION-208'`); err != nil {
		t.Fatalf("write the task as the upgrade found it: %v", err)
	}
	r.fails(fixHead, "k-ci")
	applyRefusingNothing(t, r.pool, r.engine, settledAt(fixHead, 1, "unit"))
	assertStrings(t, "the fixes", r.fixes(), nil)
	assertStrings(t, "the failures", r.failures(), nil)
	settled := toldBody("CI settled at head: \"build\" succeeded, \"unit\" failed")
	for _, role := range []claim.Role{claim.RoleArchitect, claim.RoleReviewer, claim.RoleImplementer} {
		assertStrings(t, "the "+string(role)+"'s messages", r.told(role), []string{settled})
	}
}
