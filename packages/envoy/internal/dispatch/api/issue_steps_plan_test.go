package api

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// stepsTask is one task of a read of an issue's steps as a person sees it: its id, what the plan
// says of it, and the ids of its runs in the order the read lists them.
type stepsTask struct {
	Task           string
	Needs, Planned []string
	Runs           []string
}

// stepsTasks is each task of read, an empty list read as none.
func stepsTasks(read model.IssueSteps) []stepsTask {
	tasks := make([]stepsTask, len(read.Tasks))
	for index, task := range read.Tasks {
		tasks[index] = stepsTask{Task: task.Task}
		if len(task.Needs) > 0 {
			tasks[index].Needs = task.Needs
		}
		if len(task.Planned) > 0 {
			tasks[index].Planned = task.Planned
		}
		for _, step := range task.Steps {
			tasks[index].Runs = append(tasks[index].Runs, step.ID)
		}
	}
	return tasks
}

// uploadToVersion uploads content as the document name of issue and requires that the upload
// wrote version.
func uploadToVersion(t *testing.T, handler http.Handler, issue, name, content string, version int) model.Artifact {
	t.Helper()
	artifact := createIssueDocument(t, handler, issue, name, content)
	written := dispatchRequest(t, handler, http.MethodGet, "/api/v1/artifacts/"+artifact.ID+"/versions/"+strconv.Itoa(version), nil, "alice")
	if written.Code != http.StatusOK {
		t.Fatalf("the upload of %s wrote no version %d: status=%d body=%s", name, version, written.Code, written.Body.String())
	}
	return artifact
}

// startRun writes the start of a run of step place of task, in the issue's first generation, at
// started, and answers its id.
func startRun(t *testing.T, handler http.Handler, project, issue, task, role string, place int, started time.Time) string {
	t.Helper()
	id := uuid.NewString()
	putStepOK(t, handler, issue, id, map[string]any{
		"actor": daemonSession(project), "task": task, "step": place, "kind": "step", "role": role,
		"generation": 1, "started_at": started,
	})
	return id
}

// The read of an issue's steps lists the tasks of its newest plan first, in the plan's order, each
// with the tasks it needs and the steps the plan gives it, whether a step of it ran or not; then
// each other task with a run, by its first run. An owner's plan names its needs in the document
// Legion took, at the version it took, so a later version of the document changes nothing of the
// read. The plan of the default steps is one task, which needs none, and an issue with no plan
// lists its tasks by their first run, as before. The fault is a task of the plan that a person
// cannot see; the control is the handler that lists the runs alone, under which b is absent.
func TestTheStepsOfAnIssueListThePlansTasks(t *testing.T) {
	handler := newTestHandler(t)
	issues := stepIssues(t, handler, "LEG", 3)
	owner, defaults, none := issues[0], issues[1], issues[2]
	daemon := daemonSession("LEG")

	taskA := ":::plan_task{#a steps=\"implementer reviewer\"}\nImplement it, then review it.\n:::\n"
	need := "dispatch://" + owner + "/artifact/plan-md#b-a"
	uploadToVersion(t, handler, owner, "plan.md", taskA, 1)
	uploadToVersion(t, handler, owner, "plan.md", taskA+"\n:::plan_task{#b steps=\"tester\" needs=\""+need+"\"}\nTest it.\n:::\n", 2)
	plan := uploadToVersion(t, handler, owner, "plan.md", taskA+"\n:::plan_task{#b steps=\"tester\"}\nTest it.\n:::\n", 3)
	// The daemon writes a node of two tasks with needs only once it runs such a plan; the read
	// lists what the node holds whichever release wrote it.
	putPlanRecordOK(t, handler, owner, uuid.NewString(), map[string]any{
		"actor": daemon, "generation": 1, "source": "owner", "artifact_id": plan.ID, "version": 2,
		"tasks": []map[string]any{{"id": "a", "steps": []string{"implementer", "reviewer"}}, {"id": "b", "steps": []string{"tester"}}},
	})
	earlier := startRun(t, handler, "LEG", owner, "earlier", "implementer", 1, stepStart)
	implementing := startRun(t, handler, "LEG", owner, "a", "implementer", 1, stepStart.Add(time.Minute))

	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+owner+"/steps", nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read the steps of %s: status=%d body=%s, want 200", owner, response.Code, response.Body.String())
	}
	want := []stepsTask{
		{Task: "a", Planned: []string{"implementer", "reviewer"}, Runs: []string{implementing}},
		{Task: "b", Needs: []string{"a"}, Planned: []string{"tester"}},
		{Task: "earlier", Runs: []string{earlier}},
	}
	if got := stepsTasks(decodeBody[model.IssueSteps](t, response)); !reflect.DeepEqual(got, want) {
		t.Fatalf("the steps of %s =\n%+v\nwant\n%+v", owner, got, want)
	}
	if body := response.Body.String(); !strings.Contains(body, `"task":"b","needs":["a"],"planned":["tester"]`) {
		t.Fatalf("the steps of %s answer %s, want b with \"needs\":[\"a\"] and \"planned\":[\"tester\"]", owner, body)
	}

	putPlanRecordOK(t, handler, defaults, uuid.NewString(), defaultPlanBody(daemon, 1))
	if got, want := stepsTasks(readSteps(t, handler, defaults)), []stepsTask{{Task: "default", Planned: defaultStepLine}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the steps of %s, whose plan is the default steps =\n%+v\nwant\n%+v", defaults, got, want)
	}

	later := startRun(t, handler, "LEG", none, "y", "implementer", 1, stepStart.Add(time.Minute))
	first := startRun(t, handler, "LEG", none, "x", "implementer", 1, stepStart)
	if got, want := stepsTasks(readSteps(t, handler, none)), []stepsTask{{Task: "x", Runs: []string{first}}, {Task: "y", Runs: []string{later}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the steps of %s, which has no plan =\n%+v\nwant\n%+v", none, got, want)
	}
}
