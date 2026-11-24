package daemon

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
)

// dispatch://INFRA-3583 R6: the release asks each running role of a pod with the text built for
// that role (supervise.ReleaseTask). The reviewer creates no commit and pushes nothing
// (roles/reviewer.md), so its ask says that it pushes nothing and names no branch; the
// implementer's ask names the issue's branch to push to. Both roles run in one pod with unpushed
// work, so the release asks both of them in one ask.
func TestProjectReleaseAsksEachRoleOfAPodWithItsOwnText(t *testing.T) {
	t.Parallel()
	plan := idleRigPlan{TakeTurn: true, Unpushed: &idleRigUnpushed{Commits: 1}, AnswerRelease: true}
	rig := newIdleRigWith(t, idleRigOptions{},
		idleRigSeed{name: "pair", roles: []claim.Role{claim.RoleImplementer, claim.RoleReviewer}, plan: &plan})
	pair := rig.issue("pair")
	rig.launchAll()

	rig.startRelease("45s")
	release, _ := rig.awaitReleaseFinished()
	line := releaseLine(t, release, pair.pod)
	if line.Outcome != api.ReleaseDeletedLost || len(line.Asked) != 2 {
		t.Fatalf("the pod's line is %+v, want deleted_lost with both roles asked", line)
	}
	push := "1. Push every commit that you want to keep to the branch legion/" + pair.key + "."
	nothing := "1. You create no commit, so you push nothing."
	for _, tc := range []struct {
		role          claim.Role
		says, saysNot string
	}{
		{claim.RoleImplementer, push, nothing},
		{claim.RoleReviewer, nothing, push},
	} {
		asks := rig.releaseAsks(pair.key, tc.role)
		if len(asks) != 1 {
			t.Fatalf("the %s was asked %d times, want once; its messages: %+v", tc.role, len(asks),
				rig.agents.role(pair.key, string(tc.role)).Messages)
		}
		if !strings.Contains(asks[0].Text, tc.says) || strings.Contains(asks[0].Text, tc.saysNot) {
			t.Errorf("the release's ask to the %s is %q, want it to say %q and not %q", tc.role, asks[0].Text, tc.says, tc.saysNot)
		}
	}
}
