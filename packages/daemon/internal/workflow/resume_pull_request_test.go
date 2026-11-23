package workflow

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
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
// request is open at head-<id>: #1 for a (which takes the place of stepRoot's #42 on that branch)
// to #5 for e, in task order.
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
		seedPR(t, pool, examplePR(root.Key, id, number+1))
	}
	return root
}

// examplePR is the open pull request #number of task id of the example plan of key, on the task's
// branch (seedExamplePlan) at head-<id>.
func examplePR(key, id string, number int) record.PullRequest {
	branch := "legion/" + key
	if id != "a" {
		branch += "." + id
	}
	return record.PullRequest{State: record.PullRequestOpen, Issue: key, Repo: "sjawhar/legion", Number: number,
		Branch: branch, HeadSHA: "head-" + id, Failing: []string{}}
}

// bootResumes is the task of role's newest start of key after the boot resumes role's claim, which
// the daemon itself suspended level with its entry (booted): the resume task the boot composes on
// the scope of the step role works (ScopeOfStep), which a worker resumed in a fresh session reads
// with no earlier task to read anything from.
func bootResumes(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, role claim.Role) string {
	t.Helper()
	project, err := claim.ProjectToken("LEGION")
	if err != nil {
		t.Fatal(err)
	}
	seedRuleSuspendedOwner(t, pool, project, key, role)
	mustSpend(t, "the boot", applyFacts(t, pool, engine)("boot", intake.DaemonBooted{}))
	resumed := lastStart(t, pool, key, role)
	if !strings.Contains(resumed, " Resume the existing phase work.") {
		t.Fatalf("the boot's start of the %s of %s = %q, want the resume of its phase", role, key, resumed)
	}
	return resumed
}

// Each step's task names the pull request of its own task, in a resume as in a start, so a worker
// resumed in a fresh session, which has no earlier task to read it from, is told the number of the
// pull request it works on, and the merger the head it may merge. Of the example plan a to e, each
// task with its own open pull request, a's #1 on the issue's own branch and e's #5 on
// legion/LEGION-208.e: the merger of e, the task that merges, resumed by the boot while a to d are
// done, is told ` Pull request: #5.` right after the lines of where its work stands, and no other
// number; resumed at the head a merge forward landed past the review that the reviewer is asked to
// read, it is told after that line to merge the head only once the reviewer's approval of it is on
// the pull request, as its start is; the reviewer of c, resumed by the boot while c stands at its
// review, is told c's ` Pull request: #3.`; and the start of c's review, at the completion of c's
// implementation, names #3 once.
//
// The fault it catches is a resumed merger told no number, which reads the pull request on
// legion/<KEY>, a's, by the prompts' fallback (`prompts/go/worker-common.md`), and merges it (its
// control: the resume task composed with no pull request, as before each step's task named its
// own, under which the resumed merger names no number), and a resumed merger not told that the head
// it stands at waits for the reviewer's approval (its control: the resume without the merge forward
// sentence, under which the resumed merger is not told it).
func TestAResumedStepNamesItsTasksPullRequest(t *testing.T) {
	done := func(at int) exampleStand { return exampleStand{state: record.TaskDone, place: atStep(at)} }
	waiting := exampleStand{state: record.TaskWaiting}
	atMerge := exampleStand{state: record.TaskRunning, place: record.TaskPlace{Kind: record.PlaceMerge}}
	namesOnce := func(t *testing.T, who, task string, number int) {
		t.Helper()
		if want := fmt.Sprintf(" Pull request: #%d.", number); !strings.Contains(task, want) || strings.Count(task, " Pull request: #") != 1 {
			t.Fatalf("the %s's task = %q, want it to name its task's pull request #%d once, and no other", who, task, number)
		}
	}
	const mergerSteps = " Task: e. Steps: 1 implementer:i, 2 tester:t then Legion's retro and merge."

	t.Run("the merger of the task that merges, resumed by the boot", func(t *testing.T) {
		pool, engine := migratedPool(t), textEngine()
		root := seedExamplePlan(t, pool, phase.Merging, map[string]exampleStand{"a": done(2), "b": done(3), "c": done(2), "d": done(2), "e": atMerge})

		merger := bootResumes(t, pool, engine, root.Key, claim.RoleMerger)
		namesOnce(t, "resumed merger of e", merger, 5)
		if want := mergerSteps + " Pull request: #5. Resume the existing phase work."; !strings.HasSuffix(merger, want) {
			t.Fatalf("the resumed merger's task = %q, want it to end %q: its pull request right after where its work stands", merger, want)
		}
	})

	t.Run("the merger resumed at a merge forward the reviewer is asked to read", func(t *testing.T) {
		pool, engine := migratedPool(t), textEngine()
		root := seedExamplePlan(t, pool, phase.Merging, map[string]exampleStand{"a": done(2), "b": done(3), "c": done(2), "d": done(2), "e": atMerge})
		landed := examplePR(root.Key, "e", 5)
		landed.MergeForward = &record.MergeForward{Head: "head-e-before", Base: "main", Phase: phase.Merging, Why: record.ConflictMergeNext,
			AskedAt: stepClock, Landed: landed.HeadSHA, ReviewAsked: true}
		seedPR(t, pool, landed)

		merger := bootResumes(t, pool, engine, root.Key, claim.RoleMerger)
		namesOnce(t, "resumed merger of e", merger, 5)
		if want := mergerSteps + " Pull request: #5. Head head-e is the merge of main forward that landed after the review, and the reviewer is asked to read it:" +
			" merge head head-e only once the reviewer's approval of it is on the pull request. Resume the existing phase work."; !strings.HasSuffix(merger, want) {
			t.Fatalf("the resumed merger's task = %q, want it to end %q", merger, want)
		}
	})

	t.Run("the reviewer of a task that does not merge, resumed by the boot", func(t *testing.T) {
		pool, engine := migratedPool(t), textEngine()
		root := seedExamplePlan(t, pool, phase.Reviewing, map[string]exampleStand{
			"a": done(2), "b": done(3), "c": {state: record.TaskRunning, place: atStep(2)}, "d": waiting, "e": waiting,
		})

		reviewer := bootResumes(t, pool, engine, root.Key, claim.RoleReviewer)
		namesOnce(t, "resumed reviewer of c", reviewer, 3)
		if want := " Task: c. Step 2 of 2: reviewer:r. Steps: 1 implementer:i, 2 reviewer:r. Pull request: #3. Resume the existing phase work."; !strings.Contains(reviewer, want) {
			t.Fatalf("the resumed reviewer's task = %q, want it to hold %q", reviewer, want)
		}
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
