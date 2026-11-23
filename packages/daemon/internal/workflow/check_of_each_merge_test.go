package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// twoChecks is a closed root LEGION-208 holding two open production checks, of two merges, each at
// look 1: the first opened at checkOpened, the second an hour later, each bound checkBound after
// its open. lookOpen says whether each check's look 1 is still open, first then second. It
// answers the two checks as seeded.
func twoChecks(t *testing.T, rig checkRig, lookOpen [2]bool) (first, second record.ProductionCheck) {
	t.Helper()
	issue := record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208",
		Project: "LEGION", Title: "root", Phase: phase.Done, Generation: 7, Status: "done", Rank: "U", LastDispatchSeq: 1}
	seedIssue(t, rig.pool, issue)
	check := func(number int, sha string, opened time.Time, open bool) record.ProductionCheck {
		return record.ProductionCheck{Issue: issue.Key, Project: issue.Project, Tree: issue.Tree, Repo: "sjawhar/legion", Number: number, MergeSHA: sha,
			Role: claim.RoleTester, OpenedAt: opened, BoundAt: opened.Add(checkBound), Looks: 1, LookOpen: open, LookWakeAt: &opened,
			LastWakeKind: wakeMerge, LastWakeDetail: sha, LastWakeAt: &opened}
	}
	first = check(1, "merge-1", checkOpened, lookOpen[0])
	second = check(2, "merge-2", checkOpened.Add(time.Hour), lookOpen[1])
	seedRecord(t, rig.pool, func(tx pgx.Tx) error {
		if err := record.NewStore().PutProductionCheck(t.Context(), tx, first); err != nil {
			return err
		}
		return record.NewStore().PutProductionCheck(t.Context(), tx, second)
	})
	return first, second
}

// checkOpenedAt is issue's check opened at opened, as recorded.
func checkOpenedAt(t *testing.T, rig checkRig, issue string, opened time.Time) record.ProductionCheck {
	t.Helper()
	var check *record.ProductionCheck
	seedRecord(t, rig.pool, func(tx pgx.Tx) error {
		var err error
		check, err = record.NewStore().ProductionCheckOf(t.Context(), tx, issue, opened)
		return err
	})
	if check == nil {
		t.Fatalf("%s holds no check opened at %s", issue, opened)
	}
	return *check
}

// Each row and report of a check acts on the check it names, where an issue holds two: a look's
// end names its check, a report names its check, and a report that names none — as the route sends
// until it names the look's — acts on the newest. The bound's row names no check, and the record
// decides which bound came due: each open check whose last look has not started and whose bound is
// not after now starts its last look, another is left as it stands, and while such a check has a
// look open the issue gets one row at the look bound, however many checks it defers.
func TestEachRowOfACheckActsOnTheCheckItNames(t *testing.T) {
	t.Run("a look's end ends the look of the check it names", func(t *testing.T) {
		rig := newCheckRig(t)
		first, second := twoChecks(t, rig, [2]bool{true, true})
		rig.apply("end-first", intake.CheckLookEnded{Issue: first.Issue, Look: 1, Cause: "look bound", OpenedAt: first.OpenedAt})
		if got := checkOpenedAt(t, rig, first.Issue, first.OpenedAt); got.LookOpen || !got.Open() {
			t.Fatalf("the first check after its look's end = %+v, want its look ended and the check open", got)
		}
		if got := checkOpenedAt(t, rig, second.Issue, second.OpenedAt); !got.LookOpen {
			t.Fatalf("the second check after the first's look ended = %+v, want its look 1 left open", got)
		}
	})
	t.Run("a report closes the check it names", func(t *testing.T) {
		rig := newCheckRig(t)
		first, second := twoChecks(t, rig, [2]bool{true, true})
		if result := rig.apply("pass-second", intake.CheckReport{Issue: second.Issue, Look: 1, Verdict: "pass", Summary: "driven in production", OpenedAt: second.OpenedAt}); result.Refusal != nil {
			t.Fatalf("the report of the second check = %+v, want it recorded", result.Refusal)
		}
		if got := checkOpenedAt(t, rig, second.Issue, second.OpenedAt); got.Open() || got.Outcome != record.CheckRecorded {
			t.Fatalf("the second check after its pass = %+v, want it closed as recorded", got)
		}
		if got := checkOpenedAt(t, rig, first.Issue, first.OpenedAt); !got.Open() || !got.LookOpen {
			t.Fatalf("the first check after the second's pass = %+v, want it open at its look 1", got)
		}
	})
	t.Run("a report that names no check acts on the newest", func(t *testing.T) {
		rig := newCheckRig(t)
		first, second := twoChecks(t, rig, [2]bool{true, true})
		if result := rig.apply("pass-unnamed", intake.CheckReport{Issue: second.Issue, Look: 1, Verdict: "pass", Summary: "driven in production"}); result.Refusal != nil {
			t.Fatalf("the report that names no check = %+v, want it recorded", result.Refusal)
		}
		if got := checkOpenedAt(t, rig, second.Issue, second.OpenedAt); got.Open() {
			t.Fatalf("the newest check after a report that names none = %+v, want it closed", got)
		}
		if got := checkOpenedAt(t, rig, first.Issue, first.OpenedAt); !got.Open() {
			t.Fatalf("the first check after a report that names none = %+v, want it open", got)
		}
	})
	t.Run("the bound of the first starts the first's last look alone", func(t *testing.T) {
		rig := newCheckRig(t)
		first, second := twoChecks(t, rig, [2]bool{false, false})
		rig.clock.now = first.BoundAt.Add(time.Minute)
		rig.apply("bound", intake.CheckBound{Issue: first.Issue})
		if got := checkOpenedAt(t, rig, first.Issue, first.OpenedAt); !got.Last || got.Looks != 2 || !got.LookOpen {
			t.Fatalf("the first check past its bound = %+v, want its last look, look 2, started", got)
		}
		if got := checkOpenedAt(t, rig, second.Issue, second.OpenedAt); got.Last || got.Looks != 1 || got.LookOpen {
			t.Fatalf("the second check before its bound = %+v, want it at look 1 as it stood", got)
		}
		if rows := issueOutboxOf(t, rig.pool, first.Issue, string(record.OutboxKindCheckBound)); len(rows) != 0 {
			t.Fatalf("check_bound rows after the first's bound = %d, want none: the second's own row comes at its bound", len(rows))
		}
	})
	t.Run("past both bounds the first starts its last look and the open look defers once", func(t *testing.T) {
		rig := newCheckRig(t)
		first, second := twoChecks(t, rig, [2]bool{false, true})
		rig.clock.now = second.BoundAt.Add(time.Minute)
		rig.apply("bound", intake.CheckBound{Issue: first.Issue})
		if got := checkOpenedAt(t, rig, first.Issue, first.OpenedAt); !got.Last || !got.LookOpen {
			t.Fatalf("the first check past both bounds = %+v, want its last look started", got)
		}
		if got := checkOpenedAt(t, rig, second.Issue, second.OpenedAt); got.Last || got.Looks != 1 {
			t.Fatalf("the second check with its look open = %+v, want it at look 1, its last look not started", got)
		}
		rows := issueOutboxOf(t, rig.pool, first.Issue, string(record.OutboxKindCheckBound))
		if len(rows) != 1 || !rows[0].nextAt.Equal(rig.clock.now.Add(CheckLookBound)) {
			t.Fatalf("check_bound rows after both bounds = %+v, want one at the look bound %s", rows, rig.clock.now.Add(CheckLookBound))
		}
	})
	t.Run("two open looks past both bounds queue one row for the issue", func(t *testing.T) {
		rig := newCheckRig(t)
		first, second := twoChecks(t, rig, [2]bool{true, true})
		rig.clock.now = second.BoundAt.Add(time.Minute)
		rig.apply("bound", intake.CheckBound{Issue: first.Issue})
		for _, check := range []record.ProductionCheck{first, second} {
			if got := checkOpenedAt(t, rig, check.Issue, check.OpenedAt); got.Last || got.Looks != 1 {
				t.Fatalf("the check of #%d with its look open = %+v, want it at look 1, its last look not started", check.Number, got)
			}
		}
		if rows := issueOutboxOf(t, rig.pool, first.Issue, string(record.OutboxKindCheckBound)); len(rows) != 1 {
			t.Fatalf("check_bound rows after both bounds with both looks open = %d, want one for the issue", len(rows))
		}
	})
}

// openMerge opens issue's check of the merge of #number at sha, as a merge's close opens it
// (openCheck), at the rig's clock.
func openMerge(t *testing.T, rig checkRig, issue record.Issue, number int, sha string) {
	t.Helper()
	seedRecord(t, rig.pool, func(tx pgx.Tx) error {
		ctx, _ := intake.WithCommitHooks(context.Background())
		return rig.engine.openCheck(ctx, tx, issue, record.PullRequest{Issue: issue.Key, Repo: "sjawhar/legion", Number: number, MergeSHA: sha}, false)
	})
}

// checksOfIssue is every check of issue, newest opened first, as recorded.
func checksOfIssue(t *testing.T, rig checkRig, issue string) []record.ProductionCheck {
	t.Helper()
	var checks []record.ProductionCheck
	seedRecord(t, rig.pool, func(tx pgx.Tx) error {
		var err error
		checks, err = record.NewStore().ProductionChecksOf(t.Context(), tx, issue)
		return err
	})
	return checks
}

// Each merge of one run opens a check of its own, of the issue's run, and leaves the run's other
// checks open beside it; a merge seen again — whichever of the run's checks is its — opens nothing.
// The merge of a later run closes each check of an earlier run still open as not run, with its
// look closed: nothing would look at it again, and an open check left so would keep a tree's
// linger waiting for ever (closeCheck re-arms the linger's close only when the tree's last open
// check closes).
func TestEachMergeOfARunOpensItsOwnCheckAndALaterRunsClosesTheEarlier(t *testing.T) {
	rig := newCheckRig(t)
	issue := record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208",
		Project: "LEGION", Title: "root", Phase: phase.Implementing, Generation: 7, Status: "in_progress", Rank: "U", LastDispatchSeq: 1}
	seedIssue(t, rig.pool, issue)
	openMerge(t, rig, issue, 1, "merge-1")
	rig.clock.now = checkOpened.Add(time.Hour)
	openMerge(t, rig, issue, 2, "merge-2")
	rig.clock.now = checkOpened.Add(2 * time.Hour)
	openMerge(t, rig, issue, 1, "merge-1")

	checks := checksOfIssue(t, rig, issue.Key)
	if len(checks) != 2 || checks[0].Number != 2 || checks[1].Number != 1 {
		t.Fatalf("the checks of %s after two merges of its run and the first seen again = %+v, want #2's and #1's alone", issue.Key, checks)
	}
	for _, check := range checks {
		if !check.Open() || check.Generation != 7 {
			t.Fatalf("the check of #%d after two merges of one run = %+v, want it open, of run 7", check.Number, check)
		}
	}

	later := issue
	later.Generation = 8
	if _, err := rig.pool.Exec(context.Background(), "update issues set generation = 8 where key = $1", issue.Key); err != nil {
		t.Fatal(err)
	}
	rig.clock.now = checkOpened.Add(3 * time.Hour)
	openMerge(t, rig, later, 3, "merge-3")
	checks = checksOfIssue(t, rig, issue.Key)
	if len(checks) != 3 || checks[0].Number != 3 || !checks[0].Open() || checks[0].Generation != 8 {
		t.Fatalf("the checks of %s after a merge of run 8 = %+v, want #3's open first, of run 8", issue.Key, checks)
	}
	for _, check := range checks[1:] {
		if check.Open() || check.Outcome != record.CheckNotRun || check.LookOpen {
			t.Fatalf("the check of #%d of run 7 after a merge of run 8 = %+v, want it closed not_run with its look closed", check.Number, check)
		}
	}
}

// A check of the run that holds the issue looks while the issue runs: its agent's wake starts its
// next look, and its bound starts its last look, as for an issue that left the workflow. A check
// of an earlier run waits instead, as every check of an issue back in the workflow did before: its
// wake is queued again a look bound later, and its bound closes it as not run.
func TestACheckOfTheRunThatHoldsTheIssueLooksWhileItRuns(t *testing.T) {
	for _, tc := range []struct {
		name       string
		generation uint64
		looks      bool
	}{
		{name: "a check of the run", generation: 7, looks: true},
		{name: "a check of an earlier run", generation: 6, looks: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newCheckRig(t)
			issue := record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208",
				Project: "LEGION", Title: "root", Phase: phase.Implementing, Generation: 7, Status: "in_progress", Rank: "U", LastDispatchSeq: 1}
			seedIssue(t, rig.pool, issue)
			wake := checkOpened.Add(3 * time.Hour)
			check := record.ProductionCheck{Issue: issue.Key, Project: issue.Project, Tree: issue.Tree, Repo: "sjawhar/legion", Number: 1, MergeSHA: "merge-1",
				Role: claim.RoleTester, OpenedAt: checkOpened, BoundAt: checkOpened.Add(checkBound), Looks: 1, LookWakeAt: &checkOpened,
				LastWakeKind: wakeMerge, LastWakeDetail: "merge-1", LastWakeAt: &checkOpened, WakeAt: &wake, Generation: tc.generation, Task: "a"}
			seedRecord(t, rig.pool, func(tx pgx.Tx) error { return record.NewStore().PutProductionCheck(t.Context(), tx, check) })

			rig.clock.now = wake
			rig.apply("wake", intake.CheckWakeDue{Issue: issue.Key, Look: 1, OpenedAt: checkOpened, Reason: "the deploy has not carried the merge"})
			got := checkOpenedAt(t, rig, issue.Key, checkOpened)
			starts := superviseRowsOf(t, rig.pool, issue.Key, "start")
			if tc.looks {
				if got.Looks != 2 || !got.LookOpen || len(starts) != 1 || starts[0].CheckLook != 2 || !starts[0].CheckOpenedAt.Equal(checkOpened) {
					t.Fatalf("the check at its wake = %+v with starts %+v, want look 2 started for it while its run goes on", got, starts)
				}
			} else if got.Looks != 1 || got.LookOpen || len(starts) != 0 || got.WakeAt == nil || !got.WakeAt.Equal(wake.Add(CheckLookBound)) {
				t.Fatalf("the check at its wake = %+v with starts %+v, want look 1 still ended and the wake a look bound later", got, starts)
			}

			if _, err := rig.pool.Exec(context.Background(), "update production_checks set looks = 1, look_open = false where issue = $1", issue.Key); err != nil {
				t.Fatal(err)
			}
			rig.clock.now = check.BoundAt
			rig.apply("bound", intake.CheckBound{Issue: issue.Key})
			got = checkOpenedAt(t, rig, issue.Key, checkOpened)
			if tc.looks && (!got.Open() || !got.Last || !got.LookOpen) {
				t.Fatalf("the check at its bound = %+v, want its last look started while its run goes on", got)
			}
			if !tc.looks && (got.Open() || got.Outcome != record.CheckNotRun) {
				t.Fatalf("the check at its bound = %+v, want it closed as not run", got)
			}
		})
	}
}
