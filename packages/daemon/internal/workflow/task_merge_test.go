package workflow

import (
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// atMerge and atRetro are the places of a task's merge and its retro.
var (
	atMerge = record.TaskPlace{Kind: record.PlaceMerge}
	atRetro = record.TaskPlace{Kind: record.PlaceRetro}
)

// seedAAndB writes on key, at generation, the plan of a (`implementer`, whose block says merges)
// and b (`implementer`, no needs), each with an agent of its own, standing where aAt and bAt say
// and running: a on the issue's own branch, whose pull request is the issue's (stepRoot's #42 for
// LEGION-208, seedChildPR's #43 for LEGION-209), and b on <issue branch>.b with its pull request
// bPR open.
func seedAAndB(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, generation uint64, aAt, bAt record.TaskPlace, bPR int) {
	t.Helper()
	seedTasks(t, pool, key, generation,
		planTask{id: "a", steps: []string{"implementer"}, agents: []claim.Agent{"impl-a"}, state: record.TaskRunning, place: aAt},
		planTask{id: "b", steps: []string{"implementer"}, agents: []claim.Agent{"impl-b"}, state: record.TaskRunning, place: bAt})
	markMerges(t, pool, key, "a")
	seedTaskBranch(t, pool, key, "a", claim.IssueBranch(key))
	seedTaskBranch(t, pool, key, "b", claim.IssueBranch(key)+".b")
	seedTaskPR(t, pool, engine, key, "b", bPR)
}

// actEnd is the result of the newest end of the act of kind (its retro or its merge) of task in
// key's step records, "" where none ended.
func actEnd(t *testing.T, pool *pgxpool.Pool, key, task, kind string) string {
	t.Helper()
	result := ""
	for _, write := range stepWrites(t, pool, key) {
		if write.Task == task && write.Kind == kind && write.EndedAt != nil && write.Result != nil {
			result = *write.Result
		}
	}
	return result
}

// assertNoClose fails if key's outbox holds anything of a close of the issue: a status of done, an
// issue_close or a suspension of a claim, a suspension of its pod, a linger's close, a child-closed
// notice or a production check.
func assertNoClose(t *testing.T, pool *pgxpool.Pool, key string) {
	t.Helper()
	for _, write := range statusWrites(t, pool, key) {
		if write.Status == "done" {
			t.Errorf("the status writes of %s hold %+v, want no done: the issue goes on", key, write)
		}
	}
	for _, op := range []string{"issue_close", "suspend"} {
		if rows := superviseRowsOf(t, pool, key, op); len(rows) != 0 {
			t.Errorf("the %s rows of %s = %+v, want none: the issue goes on", op, key, rows)
		}
	}
	if rows := issueSuspensions(t, pool, key); len(rows) != 0 {
		t.Errorf("the issue_suspend rows of %s = %+v, want none", key, rows)
	}
	if rows := issueOutboxOf(t, pool, key, string(record.OutboxKindLingerClose)); len(rows) != 0 {
		t.Errorf("the linger_close rows of %s = %d, want none", key, len(rows))
	}
	if containsNotice(noticeKinds(t, pool, key), "child-closed") {
		t.Errorf("the notices of %s hold child-closed, want none: the issue goes on", key)
	}
	if check := productionCheck(t, pool, key); check != nil {
		t.Errorf("the production check of %s = %+v, want none: this merge is not the issue's last", key, check)
	}
}

// The merge of one task's pull request makes that task done, and the issue goes on while another
// task runs: of a (merges), at its merge, and b (no needs), at its first step, the merge of a's pull
// request leaves a done at its merge, b as it was, and the issue in implementing, with the end of
// a's merge recorded passed. Nothing of a close is queued: no status done, no issue_close and no
// suspension, no linger's close, no child-closed notice and no production check. The same holds in
// a lingering tree, where no transition runs and the close writes its own rows.
//
// The fault it catches is the merge of a task that is not the last closing the issue (its control:
// closeMerged with no read of lastToFinish, the revision before, whose close in a lingering tree
// writes the status done, tells the architect above child-closed and opens the production check).
func TestTheMergeOfOneTaskMakesItDoneAndTheIssueGoesOn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lingering bool
	}{
		{"in a tree that runs", false},
		{"in a tree that lingers", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			key, generation, number := "LEGION-208", uint64(1), 42
			if tc.lingering {
				until := stepClock.Add(time.Hour)
				seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208",
					Project: "LEGION", Title: "root", Phase: phase.Done, Generation: 1, Status: "done", Rank: "U", LingerUntil: &until, LastDispatchSeq: 1})
				child := stepChild(t, pool, "LEGION-209", phase.Implementing, nil)
				seedChildPR(t, pool, record.PullRequestOpen)
				key, generation, number = child.Key, child.Generation, 43
			} else {
				stepRoot(t, pool, phase.Implementing)
			}
			seedAAndB(t, pool, engine, key, generation, atMerge, atStep(1), 45)
			b := tasksOf(t, pool, engine, key)["b"]

			mustSpend(t, "the merge of a's pull request", applyFacts(t, pool, engine)("merged-a",
				intake.PullRequestMerged{Repo: "sjawhar/legion", Number: number, MergeSHA: "merge-a"}))

			tasks := tasksOf(t, pool, engine, key)
			assertTask(t, tasks, "a", record.TaskDone, atMerge)
			if !reflect.DeepEqual(tasks["b"], b) {
				t.Errorf("task b after a's merge = %+v, want it as it was, %+v", tasks["b"], b)
			}
			if issue := issueOf(t, pool, engine, key); issue.Phase != phase.Implementing || issue.Hold != nil || issue.Status == "done" {
				t.Errorf("%s after a's merge stands in %s (hold %+v) with status %q, want implementing, where b stands", key, issue.Phase, issue.Hold, issue.Status)
			}
			if end := actEnd(t, pool, key, "a", record.PlaceMerge); end != "passed" {
				t.Errorf("the end of a's merge is %q, want passed", end)
			}
			assertNoClose(t, pool, key)
		})
	}
}

// Only the merge that ends the issue waits for the issues beneath it. With LEGION-209 in the
// workflow beneath LEGION-208, whose a (merges) stands at its merge and b at its retro, the merge
// of a moves a to done at once: a is not the last task to finish. b's retro passes and its merge
// starts, and the merge of b's pull request waits: b is the last, so b stays at its merge, the
// issue stands in merging, and the architect is told once, in a pr-merged notice that names the
// child. b stands at its retro, not at its first step, at a's merge: a task that passes its last
// step while a child runs, and is the last to finish, is held before its retro (beneathRetro), so
// the close wait meets the task whose retro ran beside another's, neither of them the last.
//
// The faults it catches are the merge of a task that is not the last waiting for the whole tree
// (its control: the wait with no read of lastToFinish, the revision before, under which a stays at
// its merge), and a last merge that does not wait (its control: closeMerged with no wait at all,
// under which b is done while the child runs).
func TestOnlyTheLastMergeWaitsForTheIssuesBeneath(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Retro)
	stepChild(t, pool, heldParentChild, phase.Implementing, nil)
	seedAAndB(t, pool, engine, root.Key, root.Generation, atMerge, atRetro, 45)
	apply := applyFacts(t, pool, engine)

	mustSpend(t, "the merge of a's pull request", apply("merged-a", intake.PullRequestMerged{Repo: "sjawhar/legion", Number: 42, MergeSHA: "merge-a"}))
	tasks := tasksOf(t, pool, engine, root.Key)
	assertTask(t, tasks, "a", record.TaskDone, atMerge)
	assertTask(t, tasks, "b", record.TaskRunning, atRetro)
	if kinds := noticeKinds(t, pool, root.Key); containsNotice(kinds, "pr-merged") {
		t.Fatalf("the notices of %s after a's merge = %v, want no pr-merged: a's merge waits for nobody", root.Key, kinds)
	}

	taskMoves(t, pool, engine, root.Key, "b", TriggerRetroCompleted, "")
	assertTask(t, tasksOf(t, pool, engine, root.Key), "b", record.TaskRunning, atMerge)
	mustSpend(t, "the merge of b's pull request", apply("merged-b", intake.PullRequestMerged{Repo: "sjawhar/legion", Number: 45, MergeSHA: "merge-b"}))

	assertTask(t, tasksOf(t, pool, engine, root.Key), "b", record.TaskRunning, atMerge)
	if issue := issueOf(t, pool, engine, root.Key); issue.Phase != phase.Merging {
		t.Fatalf("%s after b's merge stands in %s, want merging: its close waits for %s", root.Key, issue.Phase, heldParentChild)
	}
	want := waitingForTree(root.Key, 45, []string{heldParentChild})
	var told []string
	for _, notice := range noticesOfKind(t, pool, root.Key, "pr-merged") {
		told = append(told, notice.Reason)
	}
	if !reflect.DeepEqual(told, []string{want}) {
		t.Fatalf("the pr-merged notices of %s = %q, want the one that names %s: %q", root.Key, told, heldParentChild, want)
	}

	childLeavesDone(t, pool, engine)
	// Until Tasks 20 and 21 land, the child's leave leaves b at its merge: closeAwaitingMerge reads
	// the issue's merge pull request (record.Store.MergePullRequest), which reads the one task no
	// task needs, and this plan has two. Task 20 reads MergePullRequest on TasksThatMerge and Task 21
	// reads the pull request of the task at its merge. They turn this assertion into "b is done at its
	// merge, and the issue is done and leaves the workflow".
	assertTask(t, tasksOf(t, pool, engine, root.Key), "b", record.TaskRunning, atMerge)
}
