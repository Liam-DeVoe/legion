package workflow

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// runRecords is the start and the end record of run starts of the step at place of key's task a,
// each nil where none was written.
func runRecords(t *testing.T, r *fixRig, kind string, place int, starts int64) (start, end *record.StepRecord) {
	t.Helper()
	id := StepID("LEGION-208", 1, record.StepPlace{Task: "a", Kind: kind, Place: place}, starts)
	for _, write := range stepWrites(t, r.pool, "LEGION-208") {
		if write.ID != id {
			continue
		}
		switch {
		case isStart(write):
			start = &write
		case isEnd(write):
			end = &write
		}
	}
	return start, end
}

// commitOf is the commit a record's field names, "" for none.
func commitOf(commit *string) string {
	if commit == nil {
		return ""
	}
	return *commit
}

// A review that runs beside a fix starts at X and ends at X: the fix pushed Y while the review
// ran, and the reviewer's completion reports X, the head its review read, so the record of the
// review's run names X at both ends.
func TestAReviewBesideAFixStartsAndEndsAtX(t *testing.T) {
	r := startedFix(t, intake.FixJobRead{Name: "unit", URL: fixPage})
	r.pushed(headTwo)
	r.apply("fix-done", fixCompletion(headTwo))

	r.move(TriggerReviewApproved, "", record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleReviewer, HandoffCommit: fixHead, HandoffRecord: reviewRecord})

	start, end := runRecords(t, r, record.PlaceStep, 3, 1)
	if start == nil || commitOf(start.StartCommit) != fixHead {
		t.Fatalf("the start of the review's run = %+v, want it at %s", start, fixHead)
	}
	if end == nil || commitOf(end.EndCommit) != fixHead {
		t.Fatalf("the end of the review's run = %+v, want it at %s", end, fixHead)
	}
}

// The fix's run records X to Y: it starts at the head its check failed at and ends at the commit
// its completion reports, and its start names its check.
func TestTheFixRecordsXToY(t *testing.T) {
	r := startedFix(t, intake.FixJobRead{Name: "unit", URL: fixPage})
	r.pushed(headTwo)
	r.apply("fix-done", fixCompletion(headTwo))

	start, end := runRecords(t, r, record.PlaceStep, 1, 2)
	if start == nil || commitOf(start.StartCommit) != fixHead || commitOf(start.FixCheck) != "k-ci" {
		t.Fatalf("the start of the fix's run = %+v, want it at %s for check k-ci", start, fixHead)
	}
	if end == nil || commitOf(end.EndCommit) != headTwo {
		t.Fatalf("the end of the fix's run = %+v, want it at %s", end, headTwo)
	}
}

// A planner that starts before the issue has a pull request names no start commit: the daemon
// reads no branch head at a start, and the first implementation step opens the pull request. The
// control, kept: a step that starts while the pull request is open names its start commit, the
// pull request's head.
func TestAPlannerBeforeAnyPullRequestNamesNoStartCommit(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
		Tree: "LEGION-208", Project: "LEGION", Title: "root", Phase: phase.Admitted, Generation: 1, Status: "in_progress", Rank: "U", LastDispatchSeq: 1})
	seedTreeLifecycle(t, pool, "LEGION-208")
	seedStepsPlan(t, pool, "LEGION-208", 1, "planner", "implementer", "reviewer")
	firstStep(t, pool, engine, "LEGION-208")
	r := &fixRig{t: t, pool: pool, engine: engine}

	start, _ := runRecords(t, r, record.PlaceStep, 1, 1)
	if start == nil || start.StartCommit != nil {
		t.Fatalf("the start of the planner's run = %+v, want one naming no start commit", start)
	}

	t.Run("a step that starts with an open pull request names its head", func(t *testing.T) {
		r := newFixRig(t, returnEngine(), []string{"planner", "implementer", "reviewer"})
		start, _ := runRecords(t, r, record.PlaceStep, 1, 1)
		if start == nil || commitOf(start.StartCommit) != fixHead {
			t.Fatalf("the start of the planner's run with pull request #42 open = %+v, want it at %s", start, fixHead)
		}
	})
}
