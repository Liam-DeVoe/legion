package daemon

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
)

// The controller's set_status of todo on an issue the daemon has not recorded, which carries the
// legion label, is the controller's intake (R19's handover, R26 of dispatch://INFRA-3320): the
// call queues the todo (decision 5 of dispatch://INFRA-4730), the outbox writes it, and the
// outbox applies Dispatch's answer to it as the daemon's own write (api.ApplyOwnStatusWrite), so
// admission records the issue, stands it in the waiting line and starts it at a free slot before
// the issue's own event is delivered. On the scratch rig (the real Dispatch, Postgres and NATS)
// every event of the issue is dropped as the consumer delivers it (dropConsumedEvent), the todo's
// echo included, so nothing but the write's answer can have recorded it.
func TestControllerSetStatusTodoAdmitsAnUnrecordedLabelledIssue(t *testing.T) {
	r := newPersonHandoverRig(t, 2)
	const title = "Harbour crane telemetry"
	var dropped, droppedTodo atomic.Int32
	r.tune = func(o *overrides) {
		o.dropConsumedEvent = func(fact intake.Fact) bool {
			issue, ok := fact.(intake.DispatchIssue)
			if !ok || issue.Title != title {
				return false
			}
			dropped.Add(1)
			if issue.Status == "todo" {
				droppedTodo.Add(1)
			}
			return true
		}
	}
	r.boot()
	issue := r.create(asPerson, title, nil)
	r.addLabel(issue.Key, asPerson, "legion")

	status, body := r.d.request(http.MethodPost, "/legion/v1/grants", map[string]any{}, true)
	if status != http.StatusOK {
		t.Fatalf("mint the controller's grant = %d %s, want 200", status, body)
	}
	var grant api.GrantResponse
	if err := json.Unmarshal(body, &grant); err != nil {
		t.Fatalf("decode the controller's grant %s: %v", body, err)
	}
	status, body = r.d.request(http.MethodPost, "/legion/v1/issues/status", api.IssueStatusRequest{GrantID: grant.GrantID, Issue: issue.Key, Status: "todo"}, false)
	if status != http.StatusOK {
		t.Fatalf("the controller's set_status todo of %s = %d %s, want 200", issue.Key, status, body)
	}

	settled(t, r.d, "start "+issue.Key+" in a free slot", func(s api.State) bool {
		_, recorded := s.Issues[issue.Key]
		return recorded && slices.Contains(s.Admission.Active, issue.Key)
	})
	project, err := claim.ProjectToken(r.cfg.Project)
	if err != nil {
		t.Fatal(err)
	}
	architect, err := claim.NewToken(project, issue.Key, claim.RoleArchitect)
	if err != nil {
		t.Fatal(err)
	}
	launched := func() bool {
		return slices.ContainsFunc(r.rt.CallsOf("Spawn"), func(call fake.Call) bool { return call.Spec.Claim == architect })
	}
	for deadline := time.Now().Add(60 * time.Second); !launched(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the runtime launched %+v in 60s, want %s's architect started; the daemon holds the claims %+v", r.rt.CallsOf("Spawn"), issue.Key, r.d.claims())
		}
	}
	for deadline := time.Now().Add(30 * time.Second); droppedTodo.Load() == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no todo event of %s reached the consumer in 30s (%d of its events dropped); this proves nothing of a record made before its event", issue.Key, dropped.Load())
		}
	}
}
