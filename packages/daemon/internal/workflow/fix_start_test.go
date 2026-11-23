package workflow

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// fixPage is the address of the page of the CI job a fix's read finds.
const fixPage = "https://ci.example/run/7"

// startedFix is LEGION-208 in reviewing on `implementer tester reviewer`, k-ci failed at fixHead,
// and the read of fix 1's job answered read (its key, run, task and fix filled in here).
func startedFix(t *testing.T, read intake.FixJobRead) *fixRig {
	t.Helper()
	r := reviewing(t)
	r.fails(fixHead, "k-ci")
	r.readJob(read)
	return r
}

// readJob applies the answer of the read of fix 1's job of task a of LEGION-208.
func (r *fixRig) readJob(read intake.FixJobRead) {
	r.t.Helper()
	read.Key, read.Generation, read.Task, read.Fix = "LEGION-208", 1, "a", 1
	applyRefusingNothing(r.t, r.pool, r.engine, read)
}

// fixStarts is each start of a fix the outbox holds for LEGION-208, oldest first.
func (r *fixRig) fixStarts() []record.SuperviseRequest {
	r.t.Helper()
	var starts []record.SuperviseRequest
	for _, start := range superviseRowsOf(r.t, r.pool, "LEGION-208", "start") {
		if start.Fix > 0 {
			starts = append(starts, start)
		}
	}
	return starts
}

// latestFix is fix of LEGION-208's task a as the store holds it now.
func (r *fixRig) latestFix() record.TaskFix {
	r.t.Helper()
	var fix *record.TaskFix
	applyInTx(r.t, r.pool, r.engine, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		fix, err = r.engine.store.LatestFix(ctx, tx, "LEGION-208", 1, "a")
		return err
	})
	if fix == nil {
		r.t.Fatal("LEGION-208's task a holds no fix")
	}
	return *fix
}

// toldOfTheFix is the role message each other step of the task is sent at the fix's start, its
// job named job.
func toldOfTheFix(job string) string {
	return "A fix of task a of LEGION-208 runs: check k-ci failed at " + fixHead + " (CI job " + job + "). It is step 1 (implementer) again, " +
		"in a working copy of its own. Leave that failure to the fix: your report does not fail for it, and you run its job no more. " +
		"Push nothing to the branch of the task until the fix ended: name in your report each commit you would push, and the fix takes it."
}

// The read of the fix's failed job starts the fix: the fix runs, at an entry raised on the
// implementer's row, and the issue stays in reviewing, its record naming the fix that runs. One
// start of the implementer is queued for implementing with the fix's number and the fix's head as
// the commit its working copy is made at, waiting for no slot, and its task names the check, the
// head, the job, how it ended and its page. The implementer's place starts a second run of its
// step, whose record names the fix's check.
func TestTheFixStartsInItsOwnCopyWithItsJob(t *testing.T) {
	r := startedFix(t, intake.FixJobRead{Name: "unit", URL: fixPage})

	assertPhase(t, r.pool, phase.Reviewing)
	assertStrings(t, "the fixes", r.fixes(), []string{fixOf(1, "running", "k-ci")})
	row := roleRowOf(t, r.pool, r.engine, "LEGION-208", claim.RoleImplementer)
	fix := r.latestFix()
	if fix.Entry != row.Entry || fix.JobName != "unit" || fix.JobURL != fixPage || fix.ReadFailed != "" || fix.StartedAt == nil {
		t.Fatalf("the running fix = %+v, want its job unit at %s, started at the implementer's entry %d", fix, fixPage, row.Entry)
	}
	if issue := issueOf(t, r.pool, r.engine, "LEGION-208"); issue.RunningFix != 1 {
		t.Fatalf("the issue read names running fix %d, want 1", issue.RunningFix)
	}
	starts := r.fixStarts()
	if len(starts) != 1 {
		t.Fatalf("the starts of a fix = %+v, want one", starts)
	}
	start := starts[0]
	if start.Role != claim.RoleImplementer || start.Phase != phase.Implementing || start.Fix != 1 || start.FixFrom != fixHead ||
		start.NeedsSlot || start.Entry != row.Entry || start.Generation != 1 {
		t.Fatalf("the fix's start = %+v, want the implementer's in implementing for fix 1 from %s at entry %d, waiting for no slot", start, fixHead, row.Entry)
	}
	for _, want := range []string{
		"Phase: implementing.",
		"Step 1 of 3: implementer.",
		" This is a fix: check k-ci failed at " + fixHead + " (CI job unit ended failure, page " + fixPage + ").",
	} {
		if !strings.Contains(start.Task, want) {
			t.Fatalf("the fix's task = %q, want it to hold %q", start.Task, want)
		}
	}
	if row.EntryTask != start.Task || row.StepTask != "a" || row.StepPlace != 1 {
		t.Fatalf("the implementer's row holds task %q of step %s/%d, want the fix's task of step a/1", row.EntryTask, row.StepTask, row.StepPlace)
	}
	if got := stepStartCounts(t, r.pool, "LEGION-208"); !slices.Contains(got, "a/step/1=2") {
		t.Fatalf("the starts of LEGION-208 = %v, want a/step/1=2: the fix is the second run of the implementer's step", got)
	}
	var fixRun *record.StepRecord
	for _, write := range stepWrites(t, r.pool, "LEGION-208") {
		if isStart(write) && write.Step == 1 && write.ID == StepID("LEGION-208", 1, record.StepPlace{Task: "a", Kind: record.PlaceStep, Place: 1}, 2) {
			fixRun = &write
		}
	}
	if fixRun == nil || fixRun.FixCheck == nil || *fixRun.FixCheck != "k-ci" {
		t.Fatalf("the start record of the fix's run = %+v, want one naming its check k-ci", fixRun)
	}
}

// The fix's start stands beside the start the issue owes its reviewer: it is written outside the
// line of starts that wait for a slot. The control: written as a start of that line (the seam
// fixStartInLine), the fix's start takes the reviewer's owed start out, which the subtest asserts.
func TestAFixStartAndTheReviewersStartBothStand(t *testing.T) {
	t.Run("both stand", func(t *testing.T) {
		r := reviewing(t)
		if reviewers := roleStartsOf(t, r.pool, "LEGION-208", claim.RoleReviewer); len(reviewers) != 1 {
			t.Fatalf("the reviewer's starts before the fix = %+v, want the one the move into reviewing queued", reviewers)
		}
		r.fails(fixHead, "k-ci")
		r.readJob(intake.FixJobRead{Name: "unit", URL: fixPage})
		if reviewers := roleStartsOf(t, r.pool, "LEGION-208", claim.RoleReviewer); len(reviewers) != 1 {
			t.Fatalf("the reviewer's starts after the fix started = %+v, want the one still owed", reviewers)
		}
		if starts := r.fixStarts(); len(starts) != 1 {
			t.Fatalf("the starts of a fix = %+v, want one", starts)
		}
	})

	t.Run("the control: a fix's start in the line of starts takes the reviewer's out", func(t *testing.T) {
		r := reviewing(t)
		r.engine.fixStartInLine = true
		r.fails(fixHead, "k-ci")
		r.readJob(intake.FixJobRead{Name: "unit", URL: fixPage})
		if reviewers := roleStartsOf(t, r.pool, "LEGION-208", claim.RoleReviewer); len(reviewers) != 0 {
			t.Fatalf("the reviewer's starts after a fix's start in the line = %+v, want none: the line keeps one owed start", reviewers)
		}
	})
}

// runs is each run of a step of LEGION-208 the store counts, as `<task>/<kind>/<place> <run>`.
func (r *fixRig) runs() []string {
	r.t.Helper()
	return outboxStrings(r.t, r.pool, `select task || '/' || kind || '/' || place || ' ' || run from step_starts
		where issue = $1 order by task, kind, place`, "LEGION-208")
}

// The fix's run of the implementer's step stays open beside the review's: a later fact of the
// issue settles its runs, and the issue stands at the review's place, yet the step the fix runs
// is not over while the fix runs. The control is the tester's run, which the move into reviewing
// ended.
func TestTheFixsRunStaysOpenBesideTheReview(t *testing.T) {
	r := startedFix(t, intake.FixJobRead{Name: "unit", URL: fixPage})
	r.fails(fixHead, "k-ci2")
	assertStrings(t, "the runs", r.runs(), []string{"a/step/1 open", "a/step/2 ended", "a/step/3 open"})
}

// At the fix's start each other step of the task that was handed work in this run is told of the
// fix, by one role message: the tester and the reviewer here. The implementer's own fix is its
// task, and no step of the task's line that has not started is told.
func TestTheReviewerIsToldOfTheFix(t *testing.T) {
	r := startedFix(t, intake.FixJobRead{Name: "unit", URL: fixPage})
	for _, role := range []claim.Role{claim.RoleReviewer, claim.RoleTester} {
		assertStrings(t, "the "+string(role)+"'s messages", r.told(role), []string{toldOfTheFix("unit")})
	}
	assertStrings(t, "the implementer's messages", r.told(claim.RoleImplementer), nil)
}

// A fix whose read found no page of its job, or found no job, names its head and how to find the
// run: a job with no address of its page, and a read that failed, with the read's error. Its
// row keeps what the read answered.
func TestAFixWhoseJobReadFailedNamesTheHeadAndTheRead(t *testing.T) {
	t.Run("a read that failed", func(t *testing.T) {
		r := startedFix(t, intake.FixJobRead{ReadFailed: "Dispatch answered 503"})
		if fix := r.latestFix(); fix.State != record.FixRunning || fix.ReadFailed != "Dispatch answered 503" || fix.JobName != "" {
			t.Fatalf("the fix = %+v, want it running with the read's error kept", fix)
		}
		starts := r.fixStarts()
		want := " This is a fix: check k-ci failed at " + fixHead + " (Dispatch could not be read for its job: Dispatch answered 503; find its run from " +
			fixHead + " with dispatch results --issue LEGION-208)."
		if len(starts) != 1 || !strings.Contains(starts[0].Task, want) {
			t.Fatalf("the fix's starts = %+v, want one whose task holds %q", starts, want)
		}
		assertStrings(t, "the reviewer's messages", r.told(claim.RoleReviewer), []string{toldOfTheFix("of the check")})
	})

	t.Run("a job with no page", func(t *testing.T) {
		r := startedFix(t, intake.FixJobRead{Name: "unit"})
		starts := r.fixStarts()
		want := " This is a fix: check k-ci failed at " + fixHead + " (CI job unit; Dispatch holds no address of its page, so find its run from " +
			fixHead + " with dispatch results --issue LEGION-208)."
		if len(starts) != 1 || !strings.Contains(starts[0].Task, want) {
			t.Fatalf("the fix's starts = %+v, want one whose task holds %q", starts, want)
		}
	})
}

// A read of the job of a fix that no longer reads it changes nothing: the fix ended before the
// answer came, so no start is queued, the fix stays as it ended, and nobody is told.
func TestALateJobReadOfAnEndedFixChangesNothing(t *testing.T) {
	r := reviewing(t)
	r.fails(fixHead, "k-ci")
	onStored(t, r.pool, r.engine, "LEGION-208", func(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
		fix, err := r.engine.store.LatestFix(ctx, tx, issue.Key, issue.Generation, "a")
		if err != nil {
			return err
		}
		at := stepClock
		fix.State, fix.Result, fix.Way, fix.EndedAt = record.FixEnded, "stopped", 4, &at
		return r.engine.store.UpdateFix(ctx, tx, *fix)
	})

	r.readJob(intake.FixJobRead{Name: "unit", URL: fixPage})

	assertStrings(t, "the fixes", r.fixes(), []string{fixOf(1, "ended", "k-ci")})
	if starts := r.fixStarts(); len(starts) != 0 {
		t.Fatalf("the starts of a fix after a late read = %+v, want none", starts)
	}
	if fix := r.latestFix(); fix.JobName != "" || fix.Entry != 0 {
		t.Fatalf("the ended fix after a late read = %+v, want it as it ended", fix)
	}
	assertStrings(t, "the reviewer's messages", r.told(claim.RoleReviewer), nil)
}

// The line of starts that wait for a slot takes no fix's start: a start of a fix is written
// beside the phase's own start, never in its place.
func TestTheStartLineRefusesAFixsStart(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Reviewing)
	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		return StartOrOwe(t.Context(), tx, engine.store, root.Key, record.SuperviseRequest{Op: "start", Tree: root.Tree, Role: claim.RoleImplementer,
			Task: "Fix k-ci.", Generation: 1, Phase: phase.Implementing, Entry: 2, Fix: 1, FixFrom: fixHead}, stepClock)
	})
	if err == nil || !strings.Contains(err.Error(), "fix") {
		t.Fatalf("StartOrOwe of a fix's start = %v, want it refused", err)
	}
}
