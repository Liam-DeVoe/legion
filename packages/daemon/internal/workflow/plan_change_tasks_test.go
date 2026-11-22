package workflow

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// seedAToE writes on key the plan of tasks a to e of one step each, each step its own agent's key
// (`implementer:a` … `tester:e`; this release names one fresh agent of each role, record.NameAgents,
// so each task's step carries a key of its own): c needs b, d needs a and c, and e needs d, so e is
// the task that merges. a and c run at their step, b is done, and d and e wait.
func seedAToE(t *testing.T, pool *pgxpool.Pool, root record.Issue) {
	t.Helper()
	seedTasks(t, pool, root.Key, root.Generation,
		planTask{id: "a", steps: []string{"implementer:a"}, agents: []claim.Agent{"impl-a"}, state: record.TaskRunning, place: atStep(1)},
		planTask{id: "b", steps: []string{"implementer:b"}, agents: []claim.Agent{"impl-b"}, state: record.TaskDone, place: atStep(1)},
		planTask{id: "c", steps: []string{"implementer:c"}, agents: []claim.Agent{"impl-c"}, needs: []string{"b"}, state: record.TaskRunning, place: atStep(1)},
		planTask{id: "d", steps: []string{"implementer:d"}, agents: []claim.Agent{"impl-d"}, needs: []string{"a", "c"}, state: record.TaskWaiting},
		planTask{id: "e", steps: []string{"tester:e"}, agents: []claim.Agent{"test-e"}, needs: []string{"d"}, state: record.TaskWaiting})
}

// A stop holds only the tasks that need the stopped task: of the plan a to e, the owner's stop of c
// while a runs stops c and the step of c's agent, a goes on, d and e, which need c, stay waiting,
// and the issue is not held, since a runs. The owner is told the stop, and which tasks wait on it:
// `Tasks d, e need it and wait; each other task goes on.` Once a is done nothing runs, and the
// issue is held from c's phase for the stopped task.
//
// The fault it catches is a stop that halts every task of the issue (its control: the stop of a
// task of a plan of several holding the issue, as a plan of one task's stop does, which leaves the
// issue held while a runs).
func TestAStoppedTaskHoldsOnlyTheTasksThatNeedIt(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Implementing)
	seedAToE(t, pool, root)

	stop := ownersStop(t, root, "c")
	if result := applyFacts(t, pool, engine)("stop-c", stop); result.Refusal != nil {
		t.Fatalf("the owner's stop of c was refused: %+v", result.Refusal)
	}

	tasks := tasksOf(t, pool, engine, root.Key)
	assertTask(t, tasks, "a", record.TaskRunning, atStep(1))
	assertTask(t, tasks, "c", record.TaskStopped, atStep(1))
	assertTask(t, tasks, "d", record.TaskWaiting, record.TaskPlace{})
	assertTask(t, tasks, "e", record.TaskWaiting, record.TaskPlace{})
	if issue := issueOf(t, pool, engine, root.Key); issue.Phase != phase.Implementing || issue.Hold != nil {
		t.Fatalf("%s after c's stop stands in %s (hold %+v), want implementing, where a runs, and no hold", root.Key, issue.Phase, issue.Hold)
	}
	if stops := superviseRowsOf(t, pool, root.Key, "step_stop"); len(stops) != 1 || stops[0].Role != claim.RoleImplementer ||
		stops[0].Claim != agentClaim(t, root.Key, "impl-c") {
		t.Fatalf("the step stops of %s = %+v, want the one of c's agent impl-c", root.Key, stops)
	}
	notices := noticesOf(t, pool, root.Key, "task-stopped")
	if len(notices) != 1 || !strings.HasSuffix(notices[0].Reason, " Tasks d, e need it and wait; each other task goes on.") ||
		!strings.HasPrefix(notices[0].Reason, "task c of LEGION-208 was stopped by its owner") || notices[0].Role != claim.RoleImplementer {
		t.Fatalf("the task-stopped notices of %s = %+v, want the one of c's stop naming d and e, which wait on it", root.Key, notices)
	}

	taskMoves(t, pool, engine, root.Key, "a", TriggerImplementationReady, "")

	tasks = tasksOf(t, pool, engine, root.Key)
	assertTask(t, tasks, "a", record.TaskDone, atStep(1))
	assertTask(t, tasks, "d", record.TaskWaiting, record.TaskPlace{})
	if issue := issueOf(t, pool, engine, root.Key); issue.Phase != phase.Held || issue.Hold == nil ||
		*issue.Hold != (record.Hold{From: phase.Implementing, Reason: record.HoldTaskStopped}) {
		t.Fatalf("%s once a is done stands in %s (hold %+v), want held from implementing, c's phase, for the stopped task", root.Key, issue.Phase, issue.Hold)
	}
}
