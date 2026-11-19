package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/dispatch/api/apicode"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/workflowstore"
)

func init() { addTourSteps("steps", stepsTourSteps) }

// stepsTourSteps are the route tour's steps of an issue's step records, #4400's two routes: the
// read of every run of an issue, and the end of a run its start has made, as the issue's daemon
// writes it. A run's {id} names a stored record only to a write that cannot make one, so the
// write's missing step is the end, which names no identity, sent to an id no record has.
func stepsTourSteps() []tourStep {
	issueMissing := tourMissing{Param: "key", Value: "NOPE-404", Code: apicode.IssueNotFound}
	return []tourStep{
		{
			Method: http.MethodGet, Pattern: "/issues/{key}/steps", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				issue := tour.issue(t)
				tour.writeStep(t, issue, uuid.NewString(), tourStepStart())
				return tourRequest{Path: map[string]string{"key": issue.Key}, As: tourPerson("alice")}
			},
			Missing: []tourMissing{issueMissing},
		},
		{
			Method: http.MethodPut, Pattern: "/issues/{key}/steps/{id}", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				issue := tour.issue(t)
				run := uuid.NewString()
				tour.writeStep(t, issue, run, tourStepStart())
				return stepWriteOf(issue, run, tourStepEnd())
			},
			Missing: []tourMissing{issueMissing, {Param: "id", Value: uuid.NewString(), Code: apicode.StepNotFound}},
		},
	}
}

// tourStepStart is the start of the implementing step of an issue's first run, as the daemon writes
// it: the run's identity, which makes its record, and when it started.
func tourStepStart() map[string]any {
	return map[string]any{
		"task": "default", "step": 2, "kind": "step", "role": "implementer", "type": "implementation",
		"generation": 1, "started_at": "2026-11-16T10:00:00Z",
	}
}

// tourStepEnd is that run's end as the daemon writes it when its start has landed: how it ended
// and when, and no identity.
func tourStepEnd() map[string]any {
	return map[string]any{"result": "passed", "ended_at": "2026-11-16T10:25:00Z"}
}

// stepWriteOf is the write of body as run of issue, as its project's Legion daemon sends it: the
// shared token, naming the daemon's session in actor.
func stepWriteOf(issue tourIssue, run string, body map[string]any) tourRequest {
	body["actor"] = map[string]any{"kind": "session", "id": legionDaemonSessionOf(issue.Key)}
	return tourRequest{
		Path: map[string]string{"key": issue.Key, "id": run},
		Body: body,
		As:   tourBearer("agent-token"),
	}
}

// writeStep writes body as run of issue through the /api/v2 route, answering the record it
// answered.
func (tour *routeTour) writeStep(t *testing.T, issue tourIssue, run string, body map[string]any) model.StepRecord {
	t.Helper()
	written := tour.send(t, tour.stepRoute(t, tourStep{Method: http.MethodPut, Pattern: "/issues/{key}/steps/{id}"}),
		stepWriteOf(issue, run, body), "")
	if written.Code != http.StatusOK {
		t.Fatalf("write step %s of %s: %d %s", run, issue.Key, written.Code, written.Body.String())
	}
	return decodeBody[model.StepRecord](t, written)
}

// TestV2StepWriteOfWhatTheRecordHoldsWritesNothing: a write of a run every field of which the
// record already holds as sent is the no-op PUT of decision 8. It answers the record as it stands
// and makes no write: it answers while another transaction holds the run's row, which a write's
// lock would wait on. An end the record does not hold yet is a change, and is stored; and the list
// reads the run as the write answered it.
func TestV2StepWriteOfWhatTheRecordHoldsWritesNothing(t *testing.T) {
	tour := newRouteTour(t)
	route := tour.stepRoute(t, tourStep{Method: http.MethodPut, Pattern: "/issues/{key}/steps/{id}"})
	issue := tour.issue(t)
	run := uuid.NewString()
	started := tour.writeStep(t, issue, run, tourStepStart())

	holding, err := tour.database.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the transaction that holds the run: %v", err)
	}
	defer holding.Rollback(t.Context())
	if _, err := holding.Exec(t.Context(), `select id from workflow_nodes where kind = $1 and id = $2 for update`,
		workflowstore.KindStep, run); err != nil {
		t.Fatalf("lock the run's row: %v", err)
	}
	answered := make(chan *httptest.ResponseRecorder, 1)
	again := tour.request(t, route, stepWriteOf(issue, run, tourStepStart()), "")
	go func() { answered <- tour.serve(again) }()
	select {
	case response := <-answered:
		if response.Code != http.StatusOK {
			t.Fatalf("the start again: %d %s, want 200 with the record", response.Code, response.Body.String())
		}
		if answer := decodeBody[model.StepRecord](t, response); answer.ID != started.ID || answer.Result != "" ||
			answer.StartedAt == nil || !answer.StartedAt.Equal(*started.StartedAt) {
			t.Fatalf("the start again answered %+v, want the record as it was started, %+v", answer, started)
		}
	case <-time.After(10 * time.Second):
		_ = holding.Rollback(t.Context())
		<-answered
		t.Fatal("the start again waited on the lock of the run's row: it wrote, where an equal write writes nothing")
	}
	if err := holding.Rollback(t.Context()); err != nil {
		t.Fatalf("release the run's row: %v", err)
	}

	ended := tour.writeStep(t, issue, run, tourStepEnd())
	if ended.Result != "passed" || ended.EndedAt == nil || ended.Role != "implementer" {
		t.Fatalf("the end answered %+v, want the started record ended passed", ended)
	}
	listed := tour.send(t, tour.stepRoute(t, tourStep{Method: http.MethodGet, Pattern: "/issues/{key}/steps"}),
		tourRequest{Path: map[string]string{"key": issue.Key}, As: tourPerson("alice")}, "")
	if read := decodeBody[page[model.StepRecord]](t, listed); listed.Code != http.StatusOK || read.Total != 1 ||
		len(read.Items) != 1 || read.Items[0].ID != run || read.Items[0].Result != "passed" {
		t.Fatalf("the list answered %d %s, want the one run, ended passed", listed.Code, listed.Body.String())
	}
}
