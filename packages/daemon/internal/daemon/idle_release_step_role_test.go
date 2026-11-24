package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// An idle release reads the workspace through, and its decline tells, the worker of the issue's
// open phase where that worker is live (idleReleaseRows). At step that worker is the role of the
// running step (record.Issue.RoleAt), which the phase alone does not name: workflow.OpenPhaseRole
// answers none for step. So an issue at step whose checker runs it has its workspace read through
// the checker, and the checker is the role told that the release waits for its push, not the
// first live role. The control is an issue at implementing, whose implementer the phase names.
func TestAnIdleReleaseAtAStepReadsThroughTheRoleOfTheStep(t *testing.T) {
	t.Parallel()
	const checker = claim.Role("checker")
	for _, c := range []struct {
		name string
		view pods.IdleView
		live []claim.Role
		want claim.Role
	}{
		{name: "a step of the checker", view: pods.IdleView{Phase: phase.Step, StepRole: checker},
			live: []claim.Role{claim.RoleArchitect, checker}, want: checker},
		{name: "implementing (control)", view: pods.IdleView{Phase: phase.Implementing},
			live: []claim.Role{claim.RoleArchitect, claim.RoleImplementer}, want: claim.RoleImplementer},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.view.Issue, c.view.Tree, c.view.Generation = "LEGION-1", "LEGION-1", 2
			mark, err := idleReleaseRows(c.view, workflow.IdleDecision{Release: true, Roles: c.live}, time.Now())
			if err != nil {
				t.Fatalf("idleReleaseRows: %v", err)
			}
			if mark.Role != c.want {
				t.Errorf("the release's mark names %q, want %q, the worker of the open phase", mark.Role, c.want)
			}
			if len(mark.Rows) == 0 {
				t.Fatal("the release wrote no rows")
			}
			var check struct {
				Role claim.Role `json:"role"`
			}
			if err := json.Unmarshal(mark.Rows[0].Payload, &check); err != nil {
				t.Fatalf("decode the workspace check: %v", err)
			}
			if check.Role != c.want {
				t.Errorf("the workspace check reads through %q, want %q", check.Role, c.want)
			}
		})
	}
}
