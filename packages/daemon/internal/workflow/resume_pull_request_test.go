package workflow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// exampleStand is where one task of the example plan stands (seedExamplePlan).
type exampleStand struct {
	state record.TaskState
	place record.TaskPlace
}

// seedExamplePlan is stepRoot standing at at, holding the example plan of tasks a to e, each task
// where stands places it: a needs nothing and runs `implementer reviewer`, b needs nothing and runs
// `planner implementer reviewer`, c needs b and d needs a and c, each running `implementer
// reviewer`, and e needs d and runs `implementer tester`, so e is the task that merges. This
// release names one agent of each role for an issue, so the steps of a role share one key and one
// agent in every task. Each task works on its branch as the starts of a plan give them, a on the
// issue's own legion/LEGION-208 and each other task on legion/LEGION-208.<id>, where its own pull
// request is open: #1 for a (which takes the place of stepRoot's #42 on that branch) to #5 for e,
// in task order.
func seedExamplePlan(t *testing.T, pool *pgxpool.Pool, at phase.Phase, stands map[string]exampleStand) record.Issue {
	t.Helper()
	root := stepRoot(t, pool, at)
	task := func(id string, needs []string, steps ...string) planTask {
		agents := make([]claim.Agent, len(steps))
		for i, step := range steps {
			role, key, _ := strings.Cut(step, ":")
			agents[i] = claim.Agent(role + "-" + key)
		}
		return planTask{id: id, steps: steps, agents: agents, needs: needs, state: stands[id].state, place: stands[id].place}
	}
	seedTasks(t, pool, root.Key, root.Generation,
		task("a", nil, "implementer:i", "reviewer:r"),
		task("b", nil, "planner:p", "implementer:i", "reviewer:r"),
		task("c", []string{"b"}, "implementer:i", "reviewer:r"),
		task("d", []string{"a", "c"}, "implementer:i", "reviewer:r"),
		task("e", []string{"d"}, "implementer:i", "tester:t"))
	seedBranches(t, pool, root.Key, "a", "b", "c", "d", "e")
	for number, id := range []string{"a", "b", "c", "d", "e"} {
		branch := "legion/" + root.Key
		if id != "a" {
			branch += "." + id
		}
		seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: root.Key, Repo: "sjawhar/legion", Number: number + 1,
			Branch: branch, HeadSHA: "head-" + id, Failing: []string{}})
	}
	return root
}

// resumedOn is the resume task of the worker of task's step of key, composed as the engine composes
// every resume (resumeTaskOn: the boot's resume by rule, the ready of a claim, the silence repair,
// the promotion and an owed entry with no task of its own), on the copy of key read for task
// (Engine.view): what a worker resumed in a fresh session is told, with no earlier task to read
// anything from.
func resumedOn(t *testing.T, pool *pgxpool.Pool, engine *Engine, key, task string) string {
	t.Helper()
	var text string
	onTask(t, pool, engine, key, task, func(ctx context.Context, tx pgx.Tx, view record.Issue, w Walk) error {
		var err error
		text, err = engine.resumeTaskOn(ctx, tx, view, w)
		return err
	})
	return text
}

// Each step's task names the pull request of its own task, in a resume as in a start, so a worker
// resumed in a fresh session, which has no earlier task to read it from, is told the number of the
// pull request it works on. Of the example plan a to e, each task with its own open pull request,
// a's #1 on the issue's own branch and e's #5 on legion/LEGION-208.e: the merger of e, the task
// that merges, resumed while a to d are done, is told ` Pull request: #5.`, and no other number;
// the reviewer of c, resumed while c stands at its review, is told c's ` Pull request: #3.`; and the
// start of c's review, at the completion of c's implementation, names #3 once.
//
// The fault it catches is a resumed merger told no number, which reads the pull request on
// legion/<KEY>, a's, by the prompts' fallback (`prompts/go/worker-common.md`), and merges it (its
// control: the resume task composed with no pull request, as before each step's task named its
// own, under which the resumed merger names no number).
func TestAResumedStepNamesItsTasksPullRequest(t *testing.T) {
	done := func(at int) exampleStand { return exampleStand{state: record.TaskDone, place: atStep(at)} }
	waiting := exampleStand{state: record.TaskWaiting}
	namesOnce := func(t *testing.T, who, task string, number int) {
		t.Helper()
		if want := fmt.Sprintf(" Pull request: #%d.", number); !strings.Contains(task, want) || strings.Count(task, " Pull request: #") != 1 {
			t.Fatalf("the %s's task = %q, want it to name its task's pull request #%d once, and no other", who, task, number)
		}
	}

	t.Run("the merger of the task that merges, resumed", func(t *testing.T) {
		pool, engine := migratedPool(t), textEngine()
		root := seedExamplePlan(t, pool, phase.Merging, map[string]exampleStand{
			"a": done(2), "b": done(3), "c": done(2), "d": done(2),
			"e": {state: record.TaskRunning, place: record.TaskPlace{Kind: record.PlaceMerge}},
		})

		merger := resumedOn(t, pool, engine, root.Key, "e")
		if !strings.Contains(merger, " Phase: merging.") || !strings.Contains(merger, " Resume the existing phase work.") {
			t.Fatalf("the merger's task = %q, want the resume of merging", merger)
		}
		namesOnce(t, "resumed merger of e", merger, 5)
	})

	t.Run("the reviewer of a task that does not merge, resumed", func(t *testing.T) {
		pool, engine := migratedPool(t), textEngine()
		root := seedExamplePlan(t, pool, phase.Reviewing, map[string]exampleStand{
			"a": done(2), "b": done(3), "c": {state: record.TaskRunning, place: atStep(2)}, "d": waiting, "e": waiting,
		})

		reviewer := resumedOn(t, pool, engine, root.Key, "c")
		if !strings.Contains(reviewer, " Phase: reviewing.") || !strings.Contains(reviewer, " Resume the existing phase work.") {
			t.Fatalf("the reviewer's task = %q, want the resume of reviewing", reviewer)
		}
		namesOnce(t, "resumed reviewer of c", reviewer, 3)
	})

	t.Run("the start of a step names its task's pull request once", func(t *testing.T) {
		pool, engine := migratedPool(t), textEngine()
		root := seedExamplePlan(t, pool, phase.Implementing, map[string]exampleStand{
			"a": done(2), "b": done(3), "c": {state: record.TaskRunning, place: atStep(1)}, "d": waiting, "e": waiting,
		})

		taskMoves(t, pool, engine, root.Key, "c", TriggerImplementationReady, "")

		namesOnce(t, "started reviewer of c", lastStart(t, pool, root.Key, claim.RoleReviewer), 3)
	})
}
