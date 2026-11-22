package record

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// taskPullRequest is an open pull request of task of issue on branch, numbered number.
func taskPullRequest(issue, task, branch string, number int) PullRequest {
	return PullRequest{Issue: issue, Task: task, Repo: "acme/widgets", Number: number, Branch: branch, HeadSHA: "head-" + branch,
		Failing: []string{}, State: PullRequestOpen}
}

// numbersOf is the task of each of prs and its branch without legion/, in order.
func numbersOf(prs []PullRequest) []string {
	var got []string
	for _, pr := range prs {
		got = append(got, pr.Task+"#"+strings.TrimPrefix(pr.Branch, "legion/"))
	}
	return got
}

// Each task of an issue keeps its own pull request, on its own branch: the pull request of task b
// opened after task a's stands beside it rather than taking its place, and a later write of a's
// row leaves b's as it was. Each task's row is read by the issue and the task; the row of the task
// that merges, the one task no task needs, is read by the issue alone, for one issue and for
// several at once; and every row of the issue is read in task order. An issue with no plan rows
// keeps its one row under the task "", which is the row of its merge.
func TestTwoTasksOfOneIssueKeepTwoPullRequests(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	records := NewStore()
	issue, noPlan := issueFixture(planIssue), issueFixture("LEGION-209")
	a := taskPullRequest(planIssue, "a", "legion/LEGION-208", 41)
	b := taskPullRequest(planIssue, "b", "legion/LEGION-208.b", 42)
	alone := taskPullRequest(noPlan.Key, "", "legion/LEGION-209", 43)
	inTx(t, st, func(tx pgx.Tx) {
		must(t, records.PutIssue(ctx, tx, issue))
		must(t, records.PutIssue(ctx, tx, noPlan))
		v := PlanVersion{Issue: planIssue, Generation: issue.Generation, Version: 1, ArtifactID: planAuthor, Source: PlanSourceOwner,
			GivenBy: planOwner, GivenAt: planGivenAt}
		putPlan(t, tx, v, []PlanTask{
			{Issue: planIssue, Generation: issue.Generation, Task: "a", PlanVersion: 1, BlockText: "Build the widget.", State: TaskRunning},
			{Issue: planIssue, Generation: issue.Generation, Task: "b", PlanVersion: 1, BlockText: "Test the widget.", Needs: []string{"a"},
				Merges: true, State: TaskWaiting},
		}, nil)
		must(t, records.PutPullRequest(ctx, tx, a))
		must(t, records.PutPullRequest(ctx, tx, b))
		must(t, records.PutPullRequest(ctx, tx, alone))
		a.HeadSHA = "head-a-2"
		must(t, records.PutPullRequest(ctx, tx, a))
	})
	inTx(t, st, func(tx pgx.Tx) {
		for _, want := range []PullRequest{a, b, alone} {
			got, err := records.PullRequestOfTask(ctx, tx, want.Issue, want.Task)
			must(t, err)
			if got == nil || got.Number != want.Number || got.Branch != want.Branch || got.HeadSHA != want.HeadSHA || got.Task != want.Task {
				t.Fatalf("the pull request of task %q of %s = %+v, want #%d on %s at %s: each task keeps its own row", want.Task, want.Issue, got, want.Number, want.Branch, want.HeadSHA)
			}
		}
		if got, err := records.PullRequestOfTask(ctx, tx, planIssue, "c"); err != nil || got != nil {
			t.Fatalf("the pull request of task c, which has none = %+v, %v, want none", got, err)
		}
		for _, want := range []PullRequest{b, alone} {
			got, err := records.MergePullRequest(ctx, tx, want.Issue)
			must(t, err)
			if got == nil || got.Number != want.Number {
				t.Fatalf("the merge pull request of %s = %+v, want #%d, the row of task %q", want.Issue, got, want.Number, want.Task)
			}
		}
		every, err := records.TaskPullRequests(ctx, tx, planIssue)
		must(t, err)
		if got, want := numbersOf(every), []string{"a#LEGION-208", "b#LEGION-208.b"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("the pull requests of %s = %v, want %v: each task's, in task order", planIssue, got, want)
		}
		merges, err := records.PullRequestsOf(ctx, tx, []string{planIssue, noPlan.Key})
		must(t, err)
		if len(merges) != 2 || merges[planIssue].Number != b.Number || merges[noPlan.Key].Number != alone.Number {
			t.Fatalf("the pull requests of %s and %s = %+v, want #%d and #%d, the row of each one's merge", planIssue, noPlan.Key, merges, b.Number, alone.Number)
		}
	})
}

// A branch is one task's, and the table holds one row for each branch (pull_requests_repo_branch).
// A row on a task's branch recorded under another task — the row a generation with no plan rows
// recorded under "", carried to a generation whose plan gives that branch to a task — is the row a
// new write of that branch replaces, as a write of an issue's one row always did, and the new row
// stands under its own task. A row of the issue on another branch stays.
func TestAPullRequestTakesThePlaceOfItsBranchsRowUnderAnotherTask(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	records := NewStore()
	issue := issueFixture(planIssue)
	carried := taskPullRequest(planIssue, "", "legion/LEGION-208", 41)
	other := taskPullRequest(planIssue, "b", "legion/LEGION-208.b", 42)
	opened := taskPullRequest(planIssue, "x", "legion/LEGION-208", 44)
	inTx(t, st, func(tx pgx.Tx) {
		must(t, records.PutIssue(ctx, tx, issue))
		must(t, records.PutPullRequest(ctx, tx, carried))
		must(t, records.PutPullRequest(ctx, tx, other))
	})
	inTx(t, st, func(tx pgx.Tx) {
		must(t, records.PutPullRequest(ctx, tx, opened))
	})
	inTx(t, st, func(tx pgx.Tx) {
		every, err := records.TaskPullRequests(ctx, tx, planIssue)
		must(t, err)
		if got, want := numbersOf(every), []string{"b#LEGION-208.b", "x#LEGION-208"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("the pull requests of %s after #%d on %s = %v, want %v: the branch's row under task \"\" replaced, b's kept",
				planIssue, opened.Number, opened.Branch, got, want)
		}
		got, err := records.PullRequestOfTask(ctx, tx, planIssue, "x")
		must(t, err)
		if got == nil || got.Number != opened.Number {
			t.Fatalf("the pull request of task x = %+v, want #%d", got, opened.Number)
		}
	})
}
