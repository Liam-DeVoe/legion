// going_answer_record_test.go holds what a command-bar request's landing records of its route's
// answer (goingRecordAnswer) to the answer that route gives the same request.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// goingAnswerMembers is the members of the JSON object encoded, its numbers as written.
func goingAnswerMembers(t *testing.T, encoded []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var members map[string]any
	if err := decoder.Decode(&members); err != nil {
		t.Fatalf("decode %s as a JSON object: %v", encoded, err)
	}
	return members
}

// TestGoingLandingRecordsWhatItsRouteAnsweredOnceTheWritePhaseRan: an issue's update and an ask's
// open, made through the command bar's in-process path, each record beside their landing the
// status and the value their route answers that very request, the members its write phase fills
// in included - the last_seq the update's events leave, and the id of the event that opened the
// ask - never what those members held before the Commit ran. The landing is read on the run's
// pending claim as the route answers, before the write's record keeps none of it
// (goingWithoutAnswers). The advice the route answers beside the value is no part of what its
// transaction found and left, and no landing records it.
func TestGoingLandingRecordsWhatItsRouteAnsweredOnceTheWritePhaseRan(t *testing.T) {
	rig := newGoingWritesRig(t)
	asked := createInteractionIssue(t, rig.mux, "DEMO", "Asked through the bar", "The spec.\n").Key
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	for _, tc := range []struct {
		name, action string
		// filled is the member of the answer the write phase fills in.
		filled string
		// setup is the write's arguments and its request whose answer is recorded, as method
		// and path.
		setup func(t *testing.T) (map[string]any, string)
	}{
		{
			name: "an issue's update", action: "dispatch_issue_update", filled: "last_seq",
			setup: func(t *testing.T) (map[string]any, string) {
				issue := rig.issue(t, "Titled before the bar")
				return map[string]any{"issue": issue, "title": "Titled by the bar"}, http.MethodPatch + " /api/v1/issues/" + issue
			},
		},
		{
			name: "an opened ask", action: "dispatch_ask", filled: "opened_event_id",
			setup: func(t *testing.T) (map[string]any, string) {
				return map[string]any{"issue": asked, "question": "Recorded as it was answered?"},
					http.MethodPost + " /api/v1/issues/" + asked + "/asks"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arguments, request := tc.setup(t)
			run := rig.proposed(t, "alice", false, tc.action, arguments)
			var (
				answer   *httptest.ResponseRecorder
				recorded []goingWriteRequest
				readErr  error
			)
			rig.served.inProcess = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method+" "+r.URL.Path != request {
					inner.ServeHTTP(w, r)
					return
				}
				answer = httptest.NewRecorder()
				inner.ServeHTTP(answer, r)
				// The route committed: its landing stands on the pending claim.
				read, err := rig.served.readGoingRun(context.Background(), run, model.CanonicalLogin("alice"))
				readErr = err
				for _, write := range read.Writes {
					if !write.Pending {
						continue
					}
					for _, landed := range write.Requests {
						if landed.Method+" "+landed.Path == request {
							recorded = append(recorded, landed)
						}
					}
				}
				maps.Copy(w.Header(), answer.Header())
				w.WriteHeader(answer.Code)
				_, _ = w.Write(answer.Body.Bytes())
			})
			line := rig.wrote(t, "alice", run, map[string]any{"offer": tc.action, "invocation": 0, "approved": goingApprovedPerson})
			rig.served.inProcess = inner
			if !line.Outcome.OK || answer == nil {
				t.Fatalf("the write answered %+v, with %s sent through the route table: %v, want it landed through that request", line, request, answer != nil)
			}
			if readErr != nil || len(recorded) != 1 || recorded[0].Answer == nil {
				t.Fatalf("the pending claim recorded %+v of %s (read: %v), want its one landing with the route's answer", recorded, request, readErr)
			}
			if recorded[0].Outcome.Status != answer.Code {
				t.Fatalf("the landing recorded status %d, the route answered %d", recorded[0].Outcome.Status, answer.Code)
			}
			answered := goingAnswerMembers(t, answer.Body.Bytes())
			delete(answered, "advice")
			held := goingAnswerMembers(t, recorded[0].Answer)
			filled, _ := held[tc.filled].(json.Number)
			if value, err := filled.Int64(); err != nil || value <= 0 || filled != answered[tc.filled] {
				t.Fatalf("the landing recorded %s %v, the route answered %v: want the value the write phase filled in",
					tc.filled, held[tc.filled], answered[tc.filled])
			}
			if !reflect.DeepEqual(held, answered) {
				t.Fatalf("the landing recorded %s, want what the route answered, %s", recorded[0].Answer, answer.Body.String())
			}
		})
	}
}
