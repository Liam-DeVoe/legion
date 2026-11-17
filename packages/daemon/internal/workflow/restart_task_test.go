package workflow

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// personsTodo is a person's move of key to todo in the Dispatch dashboard: the event after the
// one the record took last (stepRoot's sequence 1).
func personsTodo(key string) intake.DispatchIssue {
	return intake.DispatchIssue{Key: key, ResolvedRepo: "sjawhar/legion", Seq: 2, Type: "issue.updated", Status: "todo", Title: "root", Rank: "U"}
}

// restartedBy is the reason the worker of the first step of a task a todo starts again is told.
const restartedBy = "Reason: the issue was set to todo while its task was stopped."

// loggedEngine is returnEngine logging into a buffer the test reads.
func loggedEngine() (*Engine, *bytes.Buffer) {
	engine := returnEngine()
	logged := &bytes.Buffer{}
	engine.log = slog.New(slog.NewTextHandler(logged, nil))
	return engine, logged
}

// stoppedAtTesting is LEGION-208 walking the plan `implementer tester`, its task a stopped by its
// owner at step 2, so the root is held from testing for the stopped task.
func stoppedAtTesting(t *testing.T, pool *pgxpool.Pool, engine *Engine) record.Issue {
	t.Helper()
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "tester")
	move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
	move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
	if result := applyFacts(t, pool, engine)("stop", ownersStop(t, root.Key, "a")); result.Refusal != nil {
		t.Fatalf("the owner's stop was refused: %+v", result.Refusal)
	}
	held := issueOf(t, pool, engine, root.Key)
	if held.Phase != phase.Held || held.Hold == nil || *held.Hold != (record.Hold{From: phase.Testing, Reason: record.HoldTaskStopped}) {
		t.Fatalf("%s after the stop stands in %s with hold %+v, want held from testing for the stopped task", root.Key, held.Phase, held.Hold)
	}
	return held
}

// A person's todo on a root held because its owner stopped its task starts the task again at its
// first step, in one transaction (restartTask): the hold is cleared, the task runs at step 1 with
// its stop's reason gone, the issue stands in the phase of step 1, and that step's role is
// started at a raised entry with a task that names why; the task's pass rises by one, and no
// pair, round or hook is counted. The step the stop ended is not ended again, and the start of
// step 1 is written. The Dispatch status of step 1's phase is written over the todo Dispatch
// shows. A todo on a root held for the issues beneath it leaves the hold, and one log line says
// why. A task that is done, or left the workflow with its issue, under such a hold has nothing
// left to start: each todo is refused with its own text and starts nothing. The twin (R20): a
// todo on a root with no plan rows that a failed claim holds puts back the phase the hold is from
// and starts its role, as today.
func TestATodoOnAStoppedRootStartsItsTaskAtStepOne(t *testing.T) {
	t.Run("a task stopped at testing starts again at step 1", func(t *testing.T) {
		pool := migratedPool(t)
		engine, logged := loggedEngine()
		root := stoppedAtTesting(t, pool, engine)
		stopped := taskOf(t, pool, engine, root.Key)
		entry := roleRowOf(t, pool, engine, root.Key, claim.RoleImplementer).Entry
		writes, statuses, prompts := len(stepWrites(t, pool, root.Key)), len(statusWrites(t, pool, root.Key)), len(hookPrompts(t, pool))
		logged.Reset()

		if result := applyFacts(t, pool, engine)("todo", personsTodo(root.Key)); result.Refusal != nil {
			t.Fatalf("the todo was refused: %+v", result.Refusal)
		}

		issue := issueOf(t, pool, engine, root.Key)
		if issue.Phase != phase.Implementing || issue.Hold != nil {
			t.Fatalf("%s after the todo stands in %s with hold %+v, want implementing, the phase of step 1, with no hold", root.Key, issue.Phase, issue.Hold)
		}
		if issue.Status != "in_progress" || issue.DispatchStatus != "todo" || issue.LastDispatchSeq != 2 {
			t.Fatalf("%s after the todo has status %q, Dispatch at %q and sequence %d, want in_progress, todo and 2", root.Key, issue.Status, issue.DispatchStatus, issue.LastDispatchSeq)
		}
		task := taskOf(t, pool, engine, root.Key)
		if task.State != record.TaskRunning || task.StoppedReason != "" || task.Place != (record.TaskPlace{Kind: record.PlaceStep, Position: 1}) {
			t.Fatalf("task a after the todo is %s (%q) at %+v, want running at step 1 with no stop reason", task.State, task.StoppedReason, task.Place)
		}
		want := stopped.Counts
		want.Pass++
		if !sameReturnCounts(task.Counts, want) {
			t.Fatalf("task a counts %+v after the todo, want %+v: the pass raised by one, no pair and no round", task.Counts, want)
		}
		if got := len(hookPrompts(t, pool)); got != prompts {
			t.Fatalf("the todo fired %d hook prompts, want none", got-prompts)
		}
		row := roleRowOf(t, pool, engine, root.Key, claim.RoleImplementer)
		if row.Entry != entry+1 || !row.EntryMovedBack || !strings.Contains(row.EntryTask, restartedBy) {
			t.Fatalf("the implementer's row has entry %d (moved back %v) and task %q, want entry %d, raised as a move back, whose task holds %q",
				row.Entry, row.EntryMovedBack, row.EntryTask, entry+1, restartedBy)
		}
		starts := superviseRowsOf(t, pool, root.Key, "start")
		if start := starts[len(starts)-1]; start.Role != claim.RoleImplementer || start.Phase != phase.Implementing || start.Entry != row.Entry || start.Task != row.EntryTask {
			t.Fatalf("the last start = %+v, want the implementer's start of implementing at entry %d with its row's task", start, row.Entry)
		}
		// The implementer's step of the plan `implementer tester` is step 1 of task a, which the
		// gate's opening started once: the restart is that place's second run.
		added := stepWrites(t, pool, root.Key)[writes:]
		if len(added) != 1 || !isStart(added[0]) || added[0].Role != "implementer" || added[0].ID != StepID(root.Key, root.Generation, "a", record.PlaceStep, 1, 2) {
			t.Fatalf("the todo wrote the step records %+v, want only the start of the second run of task a's step 1", added)
		}
		var ends []string
		for _, write := range stepWrites(t, pool, root.Key) {
			if isEnd(write) && write.Role == "tester" {
				ends = append(ends, resultOf(write))
			}
		}
		if !reflect.DeepEqual(ends, []string{"stopped"}) {
			t.Fatalf("the tester's step record ends %v, want the one end of the stop, stopped", ends)
		}
		if got := statusWrites(t, pool, root.Key)[statuses:]; len(got) != 1 || got[0] != record.OrdinaryStatusWrite("in_progress", "todo", "") {
			t.Fatalf("the todo queued the status writes %+v, want the one of implementing over the todo Dispatch shows", got)
		}
		line := logged.String()
		if strings.Count(line, "workflow: a stopped task starts again at its first step") != 1 || !strings.Contains(line, "act=task_restarted") ||
			!strings.Contains(line, "the issue was set to todo while its task was stopped") || strings.Contains(line, "workflow: a held phase is released") {
			t.Fatalf("the log of the todo = %q, want one line of the task restarted, act task_restarted, naming the todo, and no release of a held phase", line)
		}
	})

	t.Run("a todo on a root held for its children leaves the hold and says why", func(t *testing.T) {
		const beneath = "LEGION-210"
		pool := migratedPool(t)
		engine, logged := loggedEngine()
		root := stepRoot(t, pool, phase.Retro)
		parent := root.Key
		seedIssue(t, pool, record.Issue{Key: beneath, Repo: root.Repo, DefaultBranch: "main", GitHubRepo: root.GitHubRepo, Tree: root.Key, Parent: &parent,
			Project: "LEGION", Title: "a child", Phase: phase.Implementing, Generation: 1, Status: "in_progress", Rank: "V"})
		heldWork{from: phase.Retro, reason: record.HoldForChildren}.stand(t, pool, root)
		task, outbox := taskOf(t, pool, engine, root.Key), outboxRowsOf(t, pool, root.Key)

		if result := applyFacts(t, pool, engine)("todo", personsTodo(root.Key)); result.Refusal != nil {
			t.Fatalf("the todo was refused: %+v", result.Refusal)
		}

		issue := issueOf(t, pool, engine, root.Key)
		if issue.Phase != phase.Held || issue.Hold == nil || *issue.Hold != (record.Hold{From: phase.Retro, Reason: record.HoldForChildren}) {
			t.Fatalf("%s after the todo stands in %s with hold %+v, want held from retro for its children still", root.Key, issue.Phase, issue.Hold)
		}
		if issue.Status != "in_progress" || issue.DispatchStatus != "todo" || issue.LastDispatchSeq != 2 {
			t.Fatalf("%s after the todo has status %q, Dispatch at %q and sequence %d, want in_progress kept, todo and 2 recorded", root.Key, issue.Status, issue.DispatchStatus, issue.LastDispatchSeq)
		}
		if after := taskOf(t, pool, engine, root.Key); !reflect.DeepEqual(after, task) {
			t.Fatalf("task a after the todo = %+v, want %+v as it stood", after, task)
		}
		if got := outboxRowsOf(t, pool, root.Key); got != outbox {
			t.Fatalf("the todo queued %d outbox rows of %s, want none: nothing starts and no status is written", got-outbox, root.Key)
		}
		if line := logged.String(); strings.Count(line, "workflow: a todo leaves the hold of a root that waits for the issues beneath it") != 1 || !strings.Contains(line, beneath) {
			t.Fatalf("the log of the todo = %q, want one line saying the hold stays, naming %s", line, beneath)
		}
	})

	// A task done, or left the workflow with its issue, under a hold for the stopped task: the
	// store's move refuses neither state, so the restart refuses each before it.
	for _, tc := range []struct {
		name, state, want string
	}{
		{"a task that is done", record.TaskDone,
			"task a of LEGION-208 is done, so nothing of it is left to start again. This restart changed nothing."},
		{"a task that left the workflow with its issue", record.TaskLeft,
			"task a of LEGION-208 left the workflow with its issue before it was done, so nothing of it is left to start again. This restart changed nothing."},
	} {
		t.Run(tc.name+" is refused and starts nothing", func(t *testing.T) {
			pool := migratedPool(t)
			engine, _ := loggedEngine()
			root := stoppedAtTesting(t, pool, engine)
			applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
				return engine.store.MoveTask(ctx, tx, root.Key, root.Generation, "a", record.TaskPlace{Kind: record.PlaceStep, Position: 2}, tc.state)
			})
			task, entry := taskOf(t, pool, engine, root.Key), roleRowOf(t, pool, engine, root.Key, claim.RoleImplementer).Entry
			writes, statuses := len(stepWrites(t, pool, root.Key)), len(statusWrites(t, pool, root.Key))

			result := applyFacts(t, pool, engine)("todo", personsTodo(root.Key))

			if result.Refusal == nil || result.Refusal.Status != 409 || result.Refusal.Code != "TASK_DONE" || result.Refusal.Message != tc.want {
				t.Fatalf("the todo on %s = %+v, want 409 TASK_DONE %q", tc.name, result.Refusal, tc.want)
			}
			issue := issueOf(t, pool, engine, root.Key)
			if issue.Phase != phase.Held || issue.Hold == nil || *issue.Hold != (record.Hold{From: phase.Testing, Reason: record.HoldTaskStopped}) ||
				issue.Status != root.Status || issue.DispatchStatus != "todo" || issue.LastDispatchSeq != 2 {
				t.Fatalf("%s after the refused todo stands in %s with hold %+v, status %q, Dispatch at %q and sequence %d, want held from testing for the stopped task, %q kept, todo and 2 recorded",
					root.Key, issue.Phase, issue.Hold, issue.Status, issue.DispatchStatus, issue.LastDispatchSeq, root.Status)
			}
			if after := taskOf(t, pool, engine, root.Key); !reflect.DeepEqual(after, task) {
				t.Fatalf("task a after the refused todo = %+v, want %+v as it stood", after, task)
			}
			if got := roleRowOf(t, pool, engine, root.Key, claim.RoleImplementer).Entry; got != entry {
				t.Fatalf("the implementer's entry is %d after the refused todo, want %d", got, entry)
			}
			if w, s := len(stepWrites(t, pool, root.Key)), len(statusWrites(t, pool, root.Key)); w != writes || s != statuses {
				t.Fatalf("the refused todo wrote %d step records and %d status writes, want none", w-writes, s-statuses)
			}
		})
	}

	t.Run("the twin: a root with no plan rows that a failed claim holds is released as today", func(t *testing.T) {
		pool := migratedPool(t)
		engine, logged := loggedEngine()
		root := stepRoot(t, pool, phase.Held)
		root.Hold = &record.Hold{From: phase.Implementing}
		seedIssue(t, pool, root)

		if result := applyFacts(t, pool, engine)("todo", personsTodo(root.Key)); result.Refusal != nil {
			t.Fatalf("the todo was refused: %+v", result.Refusal)
		}

		if issue := issueOf(t, pool, engine, root.Key); issue.Phase != phase.Implementing || issue.Hold != nil {
			t.Fatalf("%s with no plan rows after the todo stands in %s with hold %+v, want implementing with no hold", root.Key, issue.Phase, issue.Hold)
		}
		starts := startsOf(t, pool, root.Key, claim.RoleImplementer)
		if len(starts) != 1 || starts[0].Phase != phase.Implementing || !strings.Contains(starts[0].Task, "Reason: the issue was set to todo while its phase was held.") {
			t.Fatalf("the implementer's starts = %+v, want one of implementing naming the release", starts)
		}
		if got := statusWrites(t, pool, root.Key); len(got) != 1 || got[0] != record.OrdinaryStatusWrite("in_progress", "todo", "") {
			t.Fatalf("the status writes = %+v, want the one of implementing over the todo", got)
		}
		if line := logged.String(); !strings.Contains(line, "workflow: a held phase is released") || strings.Contains(line, "task_restarted") {
			t.Fatalf("the log of the todo = %q, want the release of a held phase and no restart of a task", line)
		}
	})
}
