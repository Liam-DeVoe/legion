package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A second merge of an issue opens a check of its own beside the first, and closes the first as
// not run where it is still open: the store keeps both, the first closed not_run and the second
// open, and the open list holds the second alone. An earlier check left open with nothing to look
// at it would keep a tree's linger waiting for ever (closeCheck re-arms the linger's close only
// when the tree's last open check closes).
func TestASecondMergeOfAnIssueClosesItsOpenCheck(t *testing.T) {
	rig := newCheckRig(t)
	issue := record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208",
		Project: "LEGION", Title: "root", Phase: phase.Done, Generation: 7, Status: "done", Rank: "U", LastDispatchSeq: 1}
	seedIssue(t, rig.pool, issue)
	merge := func(number int, sha string) {
		t.Helper()
		seedRecord(t, rig.pool, func(tx pgx.Tx) error {
			ctx, _ := intake.WithCommitHooks(context.Background())
			return rig.engine.openCheck(ctx, tx, issue, record.PullRequest{Issue: issue.Key, Repo: "sjawhar/legion", Number: number, MergeSHA: sha}, false)
		})
	}
	merge(1, "merge-1")
	rig.clock.now = checkOpened.Add(time.Hour)
	merge(2, "merge-2")

	var checks, open []record.ProductionCheck
	seedRecord(t, rig.pool, func(tx pgx.Tx) error {
		var err error
		if checks, err = record.NewStore().ProductionChecksOf(t.Context(), tx, issue.Key); err != nil {
			return err
		}
		open, err = record.NewStore().OpenProductionChecks(t.Context(), tx)
		return err
	})
	if len(checks) != 2 || checks[0].Number != 2 || !checks[0].Open() || checks[1].Number != 1 || checks[1].Open() || checks[1].Outcome != record.CheckNotRun || checks[1].LookOpen {
		t.Fatalf("the checks of %s after a second merge = %+v, want #2's open and #1's closed not_run with its look closed", issue.Key, checks)
	}
	if len(open) != 1 || open[0].Number != 2 {
		t.Fatalf("the open production checks = %+v, want #2's alone", open)
	}
}
