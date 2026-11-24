package record

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

// numbersOfRows is the number of each of prs, in order.
func numbersOfRows(prs []PullRequest) []int {
	numbers := []int{}
	for _, pr := range prs {
		numbers = append(numbers, pr.Number)
	}
	return numbers
}

// The merge of an issue reads the one rule of which tasks merge (StoredPlan.TasksThatMerge): the
// pull requests of an issue's merges are the rows of each task that has a merge, done or to come,
// in task id order (MergePullRequests), and the issue's merge pull request is the row of the first
// of them that is not done, else of the last (MergePullRequest, and PullRequestsOf for several
// issues). Of a (no task needs it, done at its merge, #1 merged), c (b needs it, done at its last
// step, #3 open) and b (needs c, its block says merges, at its merge, #2 open), the merges are #1
// and #2, and the issue's is #2, b's, the first that is not done; with b done at its merge it is
// #2 still, the last. A later version that adds d, needing a, takes a out of the tasks that will
// merge, and a's #1 is still among the merges: a's work merged (WorkMerged). The issue's merge is
// then d's, the first that is not done, which has no pull request yet. An issue with no plan rows
// has its one row, of the task "", for each read.
//
// The fault it catches is the merge of an issue read from the one task that no task needs, so a
// plan in which two tasks merge has no merge pull request, or the wrong one (its control: the reads
// on plan.Sinks's one task, under which the first read answers none).
func TestTheMergePullRequestsOfAnIssueAreThoseOfItsMergingTasks(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	records := NewStore()
	issue, noPlan := issueFixture(planIssue), issueFixture("LEGION-209")
	merge := TaskPlace{Kind: PlaceMerge}
	row := func(version int, task string, needs []string, merges bool, state TaskState, place TaskPlace) PlanTask {
		return PlanTask{Issue: planIssue, Generation: issue.Generation, Task: task, PlanVersion: version, BlockText: "Task " + task + ".",
			Needs: needs, Merges: merges, State: state, Place: place}
	}
	version := func(n int) PlanVersion {
		return PlanVersion{Issue: planIssue, Generation: issue.Generation, Version: n, ArtifactID: planAuthor, Source: PlanSourceOwner,
			GivenBy: planOwner, GivenAt: planGivenAt}
	}
	first := []PlanTask{
		row(1, "a", nil, false, TaskDone, merge),
		row(1, "b", []string{"c"}, true, TaskRunning, merge),
		row(1, "c", nil, false, TaskDone, TaskPlace{Kind: PlaceStep, Position: 1}),
	}
	numbers := map[string]int{"a": 1, "b": 2, "c": 3}
	alone := taskPullRequest(noPlan.Key, "", "legion/LEGION-209", 43)
	inTx(t, st, func(tx pgx.Tx) {
		must(t, records.PutIssue(ctx, tx, issue))
		must(t, records.PutIssue(ctx, tx, noPlan))
		putPlan(t, tx, version(1), first, nil)
		for _, task := range []string{"a", "b", "c"} {
			branch, err := records.AssignTaskBranch(ctx, tx, planIssue, issue.Generation, task)
			if err != nil {
				t.Fatalf("give task %s its branch: %v", task, err)
			}
			pr := taskPullRequest(planIssue, task, branch, numbers[task])
			if task == "a" {
				pr.State, pr.MergeSHA = PullRequestMerged, "merge-a"
			}
			must(t, records.PutPullRequest(ctx, tx, pr))
		}
		must(t, records.PutPullRequest(ctx, tx, alone))
	})
	reads := func(what string, merges []int, issueMerge int) {
		t.Helper()
		inTx(t, st, func(tx pgx.Tx) {
			every, err := records.MergePullRequests(ctx, tx, planIssue)
			must(t, err)
			if got := numbersOfRows(every); !slices.Equal(got, merges) {
				t.Fatalf("%s: the merge pull requests of %s = %v, want %v", what, planIssue, got, merges)
			}
			got, err := records.MergePullRequest(ctx, tx, planIssue)
			must(t, err)
			if issueMerge == 0 && got != nil || issueMerge != 0 && (got == nil || got.Number != issueMerge) {
				t.Fatalf("%s: the merge pull request of %s = %+v, want #%d (0 for none)", what, planIssue, got, issueMerge)
			}
			of, err := records.PullRequestsOf(ctx, tx, []string{planIssue, noPlan.Key})
			must(t, err)
			if pr, named := of[planIssue]; issueMerge == 0 && named || issueMerge != 0 && pr.Number != issueMerge || of[noPlan.Key].Number != alone.Number {
				t.Fatalf("%s: the merge pull requests of %s and %s = %+v, want #%d (0 for none) and #%d", what, planIssue, noPlan.Key, of, issueMerge, alone.Number)
			}
		})
	}
	reads("b at its merge", []int{1, 2}, 2)

	inTx(t, st, func(tx pgx.Tx) {
		must(t, records.MoveTask(ctx, tx, planIssue, issue.Generation, "b", merge, TaskDone))
	})
	reads("b done at its merge", []int{1, 2}, 2)

	inTx(t, st, func(tx pgx.Tx) {
		later := []PlanTask{
			row(2, "a", nil, false, TaskDone, merge),
			row(2, "b", []string{"c"}, true, TaskDone, merge),
			row(2, "c", nil, false, TaskDone, TaskPlace{Kind: PlaceStep, Position: 1}),
			row(2, "d", []string{"a"}, false, TaskWaiting, TaskPlace{}),
		}
		putPlan(t, tx, version(2), later, nil)
		stored, err := records.Plan(ctx, tx, planIssue, issue.Generation)
		must(t, err)
		if merging := stored.Merging(); !slices.Equal(merging, []string{"b", "d"}) {
			t.Fatalf("premise: the tasks that will merge after version 2 = %v, want [b d]: d needs a", merging)
		}
	})
	reads("version 2 adds d, needing a", []int{1, 2}, 0)

	inTx(t, st, func(tx pgx.Tx) {
		every, err := records.MergePullRequests(ctx, tx, noPlan.Key)
		must(t, err)
		if len(every) != 1 || every[0].Number != alone.Number || every[0].Task != "" {
			t.Fatalf("the merge pull requests of %s, which has no plan rows = %+v, want its one row #%d, of the task \"\"", noPlan.Key, every, alone.Number)
		}
		got, err := records.MergePullRequest(ctx, tx, noPlan.Key)
		must(t, err)
		if got == nil || got.Number != alone.Number {
			t.Fatalf("the merge pull request of %s, which has no plan rows = %+v, want #%d", noPlan.Key, got, alone.Number)
		}
	})
}
