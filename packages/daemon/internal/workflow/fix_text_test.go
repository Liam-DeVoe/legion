package workflow

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// fixWorkspace is the directory the runtime of these tests gives LEGION-208's working copy.
const fixWorkspace = "/work/sjawhar/legion/LEGION-208"

// withWorkspace is engine with the runtime's directory of every issue's working copy.
func withWorkspace(engine *Engine) *Engine {
	engine.cfg.WorkspaceDir = func(record.Issue) string { return fixWorkspace }
	return engine
}

// lastStart is the task of the latest start of role the outbox holds for LEGION-208, "" for none.
func (r *fixRig) lastStart(role claim.Role) string {
	r.t.Helper()
	starts := roleStartsOf(r.t, r.pool, "LEGION-208", role)
	if len(starts) == 0 {
		return ""
	}
	return starts[len(starts)-1].Task
}

// assertHolds fails unless task holds each of sentences, what naming the task.
func assertHolds(t *testing.T, what, task string, sentences ...string) {
	t.Helper()
	for _, sentence := range sentences {
		if !strings.Contains(task, sentence) {
			t.Fatalf("%s = %q, want it to hold %q", what, task, sentence)
		}
	}
}

// assertLacks fails where task holds any of sentences, what naming the task.
func assertLacks(t *testing.T, what, task string, sentences ...string) {
	t.Helper()
	for _, sentence := range sentences {
		if strings.Contains(task, sentence) {
			t.Fatalf("%s = %q, want it to hold no %q", what, task, sentence)
		}
	}
}

// The sentences of a fix's task: its working copy is the fix's own, beside the task's; the check,
// the head and the job it fixes; each other failure the fix holds; and how it pushes. The control:
// the implementer's first task, of the same step with no fix, holds none of them.
func TestTheFixTextNamesItsCopyItsFailuresAndItsPush(t *testing.T) {
	copyLine := " Working copy: branch legion/LEGION-208, directory " + fixWorkspace + "-fix."
	fixed := " This is a fix: check k-ci failed at " + fixHead + " (CI job unit ended failure, page " + fixPage + ")."
	other := " Check k-ci2 failed at " + fixHead + " too: CI job of the check ended failure."
	push := " Push only from this working copy, to the branch of the task; before you push, fetch the branch and rebase your commits onto its head where it moved. " +
		"The other steps keep the task's working copy; a commit a step beside you names in its report is in this clone by its id: " +
		"copy it onto your head with jj duplicate <commit> --onto @-, and never rebase or rewrite it, since another working copy stands on it."
	r := newFixRig(t, withWorkspace(returnEngine()), []string{"implementer", "tester", "reviewer"})
	first := r.lastStart(claim.RoleImplementer)
	assertLacks(t, "the implementer's first task", first, copyLine, " This is a fix:", other, push)
	move(t, r.pool, r.engine, "LEGION-208", TriggerImplementationReady, "", record.PhaseRow{})
	move(t, r.pool, r.engine, "LEGION-208", TriggerTesterPassed, "", record.PhaseRow{})
	r.fails(fixHead, "k-ci", "k-ci2")
	r.readJob(intake.FixJobRead{Name: "unit", URL: fixPage})

	starts := r.fixStarts()
	if len(starts) != 1 {
		t.Fatalf("the starts of a fix = %+v, want one", starts)
	}
	assertHolds(t, "the fix's task", starts[0].Task, copyLine+fixed+other, push)
}

// An implementation step that starts while checks of a CI job fail at the head names them, and
// its start gives each to the implementer (ci_failures). The control: the same start with no
// failure at the head names none.
func TestAnImplementationStepsTaskTextNamesTheFailedChecks(t *testing.T) {
	failed := " Failed CI checks at " + fixHead + ": k-ci, k-ci2."
	t.Run("the failures held at its start", func(t *testing.T) {
		r := newFixRig(t, returnEngine(), []string{"planner", "implementer", "reviewer"})
		r.fails(fixHead, "k-ci", "k-ci2")
		assertStrings(t, "the failures while the planner runs", r.failures(), nil)
		move(t, r.pool, r.engine, "LEGION-208", TriggerPlannerCompleted, "", record.PhaseRow{})
		assertHolds(t, "the implementer's task", r.lastStart(claim.RoleImplementer), failed)
		assertStrings(t, "the failures after its start", r.failures(), []string{failureOf("k-ci", "implementer", 0), failureOf("k-ci2", "implementer", 0)})
	})

	t.Run("the control: no failure at the head names none", func(t *testing.T) {
		r := newFixRig(t, returnEngine(), []string{"planner", "implementer", "reviewer"})
		move(t, r.pool, r.engine, "LEGION-208", TriggerPlannerCompleted, "", record.PhaseRow{})
		assertLacks(t, "the implementer's task", r.lastStart(claim.RoleImplementer), " Failed CI checks")
	})
}

// A step that starts while a fix of its task reads its job or runs names the fix and pushes
// nothing. The control: the same step's start with no fix names none.
func TestAStepThatStartsBesideAFixTaskTextNamesTheFix(t *testing.T) {
	beside := " A fix of this task runs: check k-ci failed at " + fixHead + ". Leave that failure to the fix. " +
		"Push nothing to the branch of the task until the fix ended: name in your report each commit you would push."
	t.Run("a fix reading its job", func(t *testing.T) {
		r := newFixRig(t, returnEngine(), []string{"implementer", "tester", "reviewer"}, TriggerImplementationReady)
		r.fails(fixHead, "k-ci")
		assertStrings(t, "the fixes", r.fixes(), []string{fixOf(1, "reading", "k-ci")})
		move(t, r.pool, r.engine, "LEGION-208", TriggerTesterPassed, "", record.PhaseRow{})
		assertHolds(t, "the reviewer's task", r.lastStart(claim.RoleReviewer), beside)
	})

	t.Run("the control: no fix names none", func(t *testing.T) {
		r := newFixRig(t, returnEngine(), []string{"implementer", "tester", "reviewer"}, TriggerImplementationReady, TriggerTesterPassed)
		assertLacks(t, "the reviewer's task", r.lastStart(claim.RoleReviewer), " A fix of this task runs")
	})
}
