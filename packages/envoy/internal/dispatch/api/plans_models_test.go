package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// One finished issue whose six step records each fall in one count of the read of its project.
// Record 1 is answered by its set model A alone and record 2 made no call: both are on their set
// model. Record 3 is answered by A and B: off. Record 4 names A and does not know its counts.
// Record 5 holds only the fields a write before set_model and answers holds. Both are unknown.
// Record 6 is a merge with no agent, which no count holds. The read answers set_model_steps
// {"on":2,"off":1,"unknown":2}, read as the bytes the route sent.
func TestTheReadOfAProjectCountsStepsOnTheirSetModel(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	issue := createTestIssue(t, handler, "ACME", "Steps on their set model")
	daemon := daemonSessionOf("ACME")

	for index, models := range []map[string]any{
		{"set_model": modelA, "model": modelA, "answers": []map[string]any{modelCalls(modelA, 7)}},
		{"set_model": modelA, "answers": []map[string]any{}},
		{"set_model": modelA, "model": modelB, "answers": []map[string]any{modelCalls(modelA, 5), modelCalls(modelB, 2)}},
		{"set_model": modelA, "model": modelA},
		{"model": modelX},
	} {
		run := map[string]any{
			"actor": daemon, "task": "default", "step": index + 1, "kind": "step", "role": "implementer",
			"type": "implementation", "generation": 1, "agent": modelsAgent,
			"started_at": at(10 * index), "result": "passed", "ended_at": at(10*index + 5),
		}
		putStepOK(t, handler, issue, uuid.NewString(), withFields(run, models))
	}
	putStepOK(t, handler, issue, uuid.NewString(), map[string]any{
		"actor": daemon, "task": "default", "step": 0, "kind": "merge", "role": "merger", "generation": 1,
		"result": "passed", "ended_at": at(60),
	})
	closeIssue(t, handler, issue)

	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/projects/ACME/plans", nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read the plans of ACME: status=%d body=%s, want 200", response.Code, response.Body.String())
	}
	var read struct {
		Issues []map[string]json.RawMessage `json:"issues"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil {
		t.Fatalf("decode the plans of ACME: %v (%s)", err, response.Body.String())
	}
	if len(read.Issues) != 1 || string(read.Issues[0]["issue"]) != `"`+issue+`"` {
		t.Fatalf("the read of ACME is %s, want %s alone", response.Body.String(), issue)
	}
	counts, carried := read.Issues[0]["set_model_steps"]
	if want := `{"on":2,"off":1,"unknown":2}`; !carried || string(counts) != want {
		t.Fatalf("%s carries set_model_steps %s (carried %t), want %s", issue, counts, carried, want)
	}
}
