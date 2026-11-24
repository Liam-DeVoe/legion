package daemon

// R7 of dispatch://INFRA-3271 (verification v15's c-forced-replacement-told), the daemon's side of a
// launch's own replacement of an issue pod: the Sandbox runtime hands the daemon what the
// replacement took (sandbox.Options.LaunchTook, runtime.TakenPod; internal/runtime/sandbox's
// launch_took_test.go holds each kind of replacement to the hand-off), and the daemon tells the
// issue's architect once, by a workspace-taken notice that names each commit the read found, or
// says that no read could be made. A read that found nothing tells nobody. The hand-off goes
// through the holder the boot binds (launchRounds), as the runtime's does.

import (
	"errors"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

func TestALaunchsOwnReplacementTellsTheArchitectWhatItTook(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		work  runtime.WorkspaceWork
		err   error
		says  string
		notes int
	}{
		{name: "a_read_that_found_a_commit", work: roundUnpushed, says: passedOverCommit, notes: 1},
		{name: "no_read_could_be_made", err: errors.New("the pod's launchers are not bound to it, so no launcher of the pod can read the workspace"),
			says: "No read of the workspace could be made", notes: 1},
		{name: "control:_a_read_that_found_nothing", work: roundClean, notes: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
			holder := &launchRounds{}
			holder.bind(rig.s.roundPodEndsAtLaunch, rig.s.tellLaunchTook)
			holder.took(rig.ctx, runtime.TakenPod{Claim: rig.tokens[claim.RoleImplementer], Issue: podUpdateIssue, PodUID: podUpdatePod,
				Why: "a facet of the pod that is fixed for its life moved, so the launching role cannot start in that pod", Work: tc.work, Err: tc.err})
			told := rig.architectNotices(record.WorkspaceTakenNotice)
			if len(told) != tc.notes {
				t.Fatalf("the architect was told %+v, want %d workspace-taken notice(s)", told, tc.notes)
			}
			if tc.notes == 1 && (!strings.Contains(told[0].Reason, tc.says) || !strings.Contains(told[0].Reason, podUpdatePod)) {
				t.Fatalf("the architect was told %q, want a notice naming the pod %s and %q", told[0].Reason, podUpdatePod, tc.says)
			}
		})
	}
}
