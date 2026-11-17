package workflow

import (
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
