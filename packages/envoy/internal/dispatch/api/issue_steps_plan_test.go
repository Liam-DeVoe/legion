package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// stepsTask is one task of a read of an issue's steps as a person sees it: its id, what the plan
// says of it, and the ids of its runs in the order the read lists them. Needs are kept as the read
// answers them, so null (nil) and [] stay apart.
type stepsTask struct {
	Task    string
	Needs   []string
	Planned []string
	Runs    []string
}

// stepsTasks is each task of read.
func stepsTasks(read model.IssueSteps) []stepsTask {
	tasks := make([]stepsTask, len(read.Tasks))
	for index, task := range read.Tasks {
		tasks[index] = stepsTask{Task: task.Task, Needs: task.Needs}
		if len(task.Planned) > 0 {
			tasks[index].Planned = task.Planned
		}
		for _, step := range task.Steps {
			tasks[index].Runs = append(tasks[index].Runs, step.ID)
		}
	}
	return tasks
}

// startRun writes the start of a run of step place of task, in generation, at started, and
// answers its id.
func startRun(t *testing.T, handler http.Handler, project, issue, task, role string, place int, generation int, started time.Time) string {
	t.Helper()
	id := uuid.NewString()
	putStepOK(t, handler, issue, id, map[string]any{
		"actor": daemonSession(project), "task": task, "step": place, "kind": "step", "role": role,
		"generation": generation, "started_at": started,
	})
	return id
}

// The read of an issue's steps lists the tasks of its newest plan first, in the plan's order, each
// with the tasks it needs and the steps the plan gives it, whether a step of it ran or not; then
// each other task with a run, by its first run. The needs are the ids the plan node records: the
// read parses no document, so the node here names a document no artifact holds, whose every read
// fails. A node written with no needs (a daemon of the release before) answers them null, which is
// not [] (needs none). An issue with no plan lists its tasks by their first run. The faults are a
// task of the plan that a person cannot see, needs read from anywhere but the node, and a node
// that recorded no needs answered as needing none; the controls are the handler that lists runs
// alone (b is absent), the handler that reads the needs from the document (its read fails), and a
// reader that answers [] for a node with none (the default task's needs are not null).
func TestTheStepsOfAnIssueListThePlansTasks(t *testing.T) {
	handler := newTestHandler(t)
	issues := stepIssues(t, handler, "LEG", 3)
	owner, defaults, none := issues[0], issues[1], issues[2]
	daemon := daemonSession("LEG")

	putPlanRecordOK(t, handler, owner, uuid.NewString(), map[string]any{
		"actor": daemon, "generation": 1, "source": "owner", "artifact_id": uuid.NewString(), "version": 2,
		"tasks": []map[string]any{
			{"id": "a", "steps": []string{"implementer", "reviewer"}, "needs": []string{}},
			{"id": "b", "steps": []string{"tester"}, "needs": []string{"a"}},
		},
	})
	earlier := startRun(t, handler, "LEG", owner, "earlier", "implementer", 1, 1, stepStart)
	implementing := startRun(t, handler, "LEG", owner, "a", "implementer", 1, 1, stepStart.Add(time.Minute))

	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+owner+"/steps", nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read the steps of %s: status=%d body=%s, want 200", owner, response.Code, response.Body.String())
	}
	want := []stepsTask{
		{Task: "a", Needs: []string{}, Planned: []string{"implementer", "reviewer"}, Runs: []string{implementing}},
		{Task: "b", Needs: []string{"a"}, Planned: []string{"tester"}},
		{Task: "earlier", Runs: []string{earlier}},
	}
	if got := stepsTasks(decodeBody[model.IssueSteps](t, response)); !reflect.DeepEqual(got, want) {
		t.Fatalf("the steps of %s =\n%+v\nwant\n%+v", owner, got, want)
	}
	body := response.Body.String()
	for _, member := range []string{`"task":"a","needs":[],`, `"task":"b","needs":["a"],"planned":["tester"]`, `"task":"earlier","needs":null,`} {
		if !strings.Contains(body, member) {
			t.Fatalf("the steps of %s answer %s, want it to hold %s", owner, body, member)
		}
	}

	putPlanRecordOK(t, handler, defaults, uuid.NewString(), defaultPlanBody(daemon, 1))
	read := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+defaults+"/steps", nil, "alice")
	if got, want := stepsTasks(decodeBody[model.IssueSteps](t, read)), []stepsTask{{Task: "default", Planned: defaultStepLine}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the steps of %s, whose plan node records no needs =\n%+v\nwant\n%+v", defaults, got, want)
	}
	if !strings.Contains(read.Body.String(), `"task":"default","needs":null,`) {
		t.Fatalf("the steps of %s answer %s, want the default task's needs null: its node recorded none", defaults, read.Body.String())
	}

	later := startRun(t, handler, "LEG", none, "y", "implementer", 1, 1, stepStart.Add(time.Minute))
	first := startRun(t, handler, "LEG", none, "x", "implementer", 1, 1, stepStart)
	if got, want := stepsTasks(readSteps(t, handler, none)), []stepsTask{{Task: "x", Runs: []string{first}}, {Task: "y", Runs: []string{later}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the steps of %s, which has no plan =\n%+v\nwant\n%+v", none, got, want)
	}
}

// A plan write records each task's needs as the ids it names, and answers them; a task written
// with no needs answers them null. A need that names no task of the plan is refused, and nothing
// is written. The fault is a node that holds a need no task of it answers to; the control is a
// write that stores the needs it is given unread.
func TestAPlanWriteRecordsEachTasksNeeds(t *testing.T) {
	handler := newTestHandler(t)
	issue := stepIssues(t, handler, "LEG", 1)[0]
	daemon := daemonSession("LEG")
	id := uuid.NewString()

	refused := putPlanRecord(t, handler, issue, id, map[string]any{
		"actor": daemon, "generation": 1, "source": "default",
		"tasks": []map[string]any{{"id": "a", "steps": []string{"implementer"}, "needs": []string{"x"}}},
	})
	requireStepRefusal(t, "a need of no task", refused, http.StatusBadRequest, codePlanInput, "task a", `"x"`)
	if tasks := readSteps(t, handler, issue).Tasks; len(tasks) != 0 {
		t.Fatalf("a refused plan write left the tasks %+v, want none", tasks)
	}

	written := putPlanRecordOK(t, handler, issue, id, map[string]any{
		"actor": daemon, "generation": 1, "source": "default",
		"tasks": []map[string]any{
			{"id": "a", "steps": []string{"implementer"}, "needs": []string{}},
			{"id": "b", "steps": []string{"tester"}, "needs": []string{"a"}},
			{"id": "c", "steps": []string{"reviewer"}},
		},
	})
	if got, want := [][]string{written.Tasks[0].Needs, written.Tasks[1].Needs, written.Tasks[2].Needs}, [][]string{{}, {"a"}, nil}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the written plan's needs = %#v, want %#v", got, want)
	}
}
