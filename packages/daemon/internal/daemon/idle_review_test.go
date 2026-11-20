package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// A release's save goes to the shim of a live role that works in the issue's workspace whenever
// one is live: the reviewer works in a clone of its own and takes no snapshot of the shared
// workspace (internal/shim's runBundle), so its bundle holds the workspace only as its last
// snapshot left it. The reviewer makes the save only when it is the one live role. Every live role
// is still stopped, in the decision's order, the first of them the release's boundary.
func TestAReleaseAsksANonReviewerForItsSaveWhenOneIsLive(t *testing.T) {
	view := pods.IdleView{Issue: "ACME-1", Tree: "ACME-1", Generation: 3, Root: pods.IdleRoot{Generation: 2}}
	for _, c := range []struct {
		roles []claim.Role
		saver claim.Role
	}{
		{[]claim.Role{claim.RoleReviewer, claim.RoleTester, claim.RoleImplementer}, claim.RoleTester},
		{[]claim.Role{claim.RoleImplementer, claim.RoleReviewer}, claim.RoleImplementer},
		{[]claim.Role{claim.RoleReviewer}, claim.RoleReviewer},
	} {
		mark, err := idleReleaseRows(view, workflow.IdleDecision{Release: true, Roles: c.roles}, time.Now())
		if err != nil {
			t.Fatalf("idleReleaseRows(%v): %v", c.roles, err)
		}
		var bundle record.IssueBundle
		if err := json.Unmarshal(mark.Rows[0].Payload, &bundle); err != nil {
			t.Fatal(err)
		}
		if mark.Role != c.saver || bundle.Role != c.saver {
			t.Errorf("with %v live the save goes to %q (mark %q), want %q", c.roles, bundle.Role, mark.Role, c.saver)
		}
		var first record.SuperviseRequest
		if err := json.Unmarshal(mark.Rows[1].Payload, &first); err != nil {
			t.Fatal(err)
		}
		if first.Role != c.roles[0] || !mark.Rows[1].Boundary || len(mark.Rows) != len(c.roles)+2 {
			t.Errorf("with %v live the stops begin with %q (boundary %v) in %d rows, want %q first as the boundary in %d",
				c.roles, first.Role, mark.Rows[1].Boundary, len(mark.Rows), c.roles[0], len(c.roles)+2)
		}
	}
}
