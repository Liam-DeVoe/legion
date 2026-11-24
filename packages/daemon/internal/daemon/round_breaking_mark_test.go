package daemon

// Which rounds break their pod (R7 of dispatch://INFRA-3271, spec v21): INFRA-3118's architect
// ruled at 04:00Z, on INFRA-341's reviewer's point 2, that a drift of the Kubernetes service
// account the pod runs as breaks the pod, and that a drift of the leak check digest does not. GPT-6.1
// Sol's non-blocking point on #5285 at 0259f8af asks that the classification come from a trusted
// mark rather than from a search of the displayed reasons: the runtime marks a reason as breaking
// where it decides the drift (runtime.BreakingDrift at the reason's start, the Sandbox runtime's
// PodDrift for the account), and the round's recorded reasons carry the mark at their start where
// any reason does. A value that a reason displays can hold the mark's words without making the
// round break its pod. And INFRA-341's reviewer's point 8: an earlier round's keep of the pod holds
// no round over a drift that breaks the pod, which no role can keep working under.
//
// Each round here is over a working copy that holds a change. A round over a drift that breaks the
// pod replaces the pod after the read and tells the issue's architect what it took; any other
// round keeps the pod.

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

const (
	// leakCheckDrift is the Sandbox runtime's reason for a pod that records another leak check
	// digest than the configured one.
	leakCheckDrift = "leak check manifest 1f0e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0, now 2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f70819"
	// imageDrift is a drift of the image, which the roles can keep working under.
	imageDrift = "image legion-worker:v22.16.2, now legion-worker:v22.17.0"
)

// answerRoundOver is answerRound over every reason of reasons at once.
func (r *podUpdateRig) answerRoundOver(reasons []string, answering ...claim.Role) {
	r.t.Helper()
	r.drifts(reasons...)
	r.tick()
	for _, role := range answering {
		r.awaitAsked(role, podUpdateRound)
	}
	for _, role := range answering {
		r.signal(role, "a1b2c3d")
	}
	r.settleAll()
}

// wantReplacedAndTold holds the rig to one replacement of the pod over the change in the working
// copy, one workspace-taken notice naming it, and no keep.
func (r *podUpdateRig) wantReplacedAndTold(what string) {
	r.t.Helper()
	r.awaitReplacement(1, what)
	for range roundSweeps {
		r.tick()
	}
	if told := r.architectNotices(record.WorkspaceTakenNotice); len(told) != 1 || !strings.Contains(told[0].Reason, roundChangeHead) {
		r.t.Fatalf("the architect was told %+v %s, want one workspace-taken notice naming the change on %s", told, what, roundChangeHead)
	}
}

func TestOnlyAReasonThatOpensOnTheMarkMakesARoundBreakItsPod(t *testing.T) {
	t.Parallel()
	roles := []claim.Role{claim.RoleImplementer, claim.RoleTester}
	for _, tc := range []struct {
		name    string
		reasons []string
		breaks  bool
	}{
		{name: "a_drift_of_the_account", reasons: []string{podUpdateAccountDrift}, breaks: true},
		{name: "a_drift_of_the_account_after_a_drift_of_the_image", reasons: []string{imageDrift, podUpdateAccountDrift}, breaks: true},
		{name: "a_drift_of_the_leak_check_alone", reasons: []string{leakCheckDrift}},
		{name: "control:_a_value_that_holds_the_mark's_words", reasons: []string{"image legion-worker:" + runtime.BreakingDrift + "v22.16.2, now legion-worker:v22.17.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester))
			rig.rt.ScriptWorkspaceWork(podUpdateIssue, roundChanged)
			rig.answerRoundOver(tc.reasons, roles...)
			if tc.breaks {
				rig.wantReplacedAndTold("of a round over a drift that breaks the pod")
				if reason, _ := rig.keptReason(); reason != "" {
					t.Fatalf("the round recorded a keep %q, want none: it replaced the pod", reason)
				}
				return
			}
			rig.roundEndsKept("over a drift the roles can keep working under", roles...)
			if reason, pod := rig.keptReason(); reason != pods.DeclineUnpushedWork || pod != podUpdatePod {
				t.Fatalf("the decline row reads %q of pod %q, want %s of the round's pod %s", reason, pod, pods.DeclineUnpushedWork, podUpdatePod)
			}
			if told := rig.architectNotices(record.WorkspaceTakenNotice); len(told) != 0 {
				t.Fatalf("the architect was told a replacement took the pod %+v, want nothing: the round kept it", told)
			}
		})
	}
}

// INFRA-341's reviewer's point 8: an earlier round kept the pod over a drift of the image, and the
// change is still in the working copy. The account then drifts too. The keep holds no round over
// that drift: the round opens, asks, and replaces the pod after its read, and the architect is told
// what the replacement took.
func TestAKeptPodGetsARoundOverADriftThatBreaksIt(t *testing.T) {
	t.Parallel()
	roles := []claim.Role{claim.RoleImplementer, claim.RoleTester}
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester))
	rig.rt.ScriptWorkspaceWork(podUpdateIssue, roundChanged)
	rig.answerRoundOver([]string{imageDrift}, roles...)
	rig.roundEndsKept("over a drift of the image", roles...)
	if reason, _ := rig.keptReason(); reason != pods.DeclineUnpushedWork {
		t.Fatalf("the decline row reads %q, want %s: this case needs the keep", reason, pods.DeclineUnpushedWork)
	}

	rig.answerRoundOver([]string{imageDrift, podUpdateAccountDrift}, roles...)
	rig.wantReplacedAndTold("of the kept pod once its account drifted")
}
