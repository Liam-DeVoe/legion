package daemon

import (
	"errors"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// A released role whose wake was refused for the round's pod, and which the daemon or the
// operator then suspended before its launch reached any pod, is still named at the end of the
// round over the pod that took the release's work: that pod restored the bundle of the workspace
// every role shares, so the role's unpushed work is on its disk, whatever suspended the role since
// (workOnRoundPod, dispatch://INFRA-4425/comment/566feff4-2b27-41f2-89a9-b1a333d97ecd). The
// order table's takeover rows suspend a role the release still holds (supervise's
// suspendedAgain); this test suspends a woken one, whose launch is under way (supervise's
// suspended). Control: a rule under which a suspension by the daemon or the operator of a claim an
// idle release had suspended takes the release's work off it, as the earlier release mark was
// cleared by such a suspension, fails every row; no row of the order table fails under it.
func TestAWokenReleasedRoleSuspendedBeforeAnyPodIsNamedWhereThePodWent(t *testing.T) {
	for _, by := range []supervise.SuspendSource{supervise.SuspendedByDaemon, supervise.SuspendedByOperator} {
		for _, end := range []roundPodEnd{endPodGone, endRecreated, endRecreatedRefusing} {
			t.Run(string(by)+": "+end.String(), func(t *testing.T) {
				rig := wokenSuspendedTo(t, end, by)
				wantRoundEnd(t, rig, end, wokenRefused, "implementer (suspended)")
			})
		}
	}
}

// wokenSuspendedTo brings a round over podUpdatePod to end, its implementer released in
// releasedPod with the tester, whose wake brought the issue's pod back as P, which took the
// release's work (wakesInto). Once the round has opened and asked the tester, the implementer's
// wake is refused for P before its agent registers anywhere, and the daemon or the operator (by)
// suspends the woken implementer, so its launch reached no pod. The tester answers, and P goes on
// its own or is recreated, with or without the exchange identity, while the implementer stays
// suspended.
func wokenSuspendedTo(t *testing.T, end roundPodEnd, by supervise.SuspendSource) *podUpdateRig {
	t.Helper()
	implementer, tester := claim.RoleImplementer, claim.RoleTester
	rig := newPodUpdateRig(t, podUpdateRole{role: implementer, state: supervise.StateQueued}, podUpdateRole{role: tester, state: supervise.StateQueued})
	rig.rt.ScriptWorkspaceWork(podUpdateIssue, runtime.WorkspaceWork{Head: passedOverHead, Commits: []string{passedOverCommit}, Count: 1})
	refusals := rig.refuseLaunches()
	rig.startInPod(implementer, releasedPod)
	rig.startInPod(tester, releasedPod)
	rig.releases(releasedPod, 1, releasedSavedAt, implementer, tester)
	rig.wakesInto(tester, podUpdatePod, roundPodMadeAt, nil)
	rig.drifts(podUpdateExchangeDrift)

	rig.tick()
	rig.awaitRound(implementer, podUpdateRound)
	rig.awaitAsked(tester, podUpdateRound)
	refusals.refuse(implementer, podUpdatePod)
	if err := rig.machineOf(implementer).Handle(rig.ctx, rig.wakeFor(implementer)); !errors.Is(err, runtime.ErrAwaitingPodRound) {
		t.Fatalf("the implementer's wake answered %v, want its launch refused for the pod's round", err)
	}
	if err := rig.machineOf(implementer).Handle(rig.ctx, supervise.RequestSuspend{Claim: rig.tokens[implementer], By: by}); err != nil &&
		!errors.Is(err, supervise.ErrSuspendHeld) {
		t.Fatalf("suspend the woken implementer by %s: %v", by, err)
	} else if err != nil {
		rig.retryLaunch()
	}
	c := rig.awaitClaim(implementer, "its suspension before any pod", func(c supervise.Claim) bool {
		return c.State == supervise.StateSuspended && c.Locator == nil && c.AwaitsPod == nil
	})
	if c.SuspendedBy != by || c.Session == "" || len(c.RegisteredPods.Pods) != 1 || c.RegisteredPods.Pods[0] != releasedPod {
		t.Fatalf("the implementer is suspended by %q with session %q and set %+v; want the %s's suspension, its session and the set of releasedPod alone",
			c.SuspendedBy, c.Session, c.RegisteredPods, by)
	}
	rig.settleAll()
	rig.signal(tester, "c0ffee")

	switch end {
	case endPodGone:
		rig.podGone()
	case endRecreated:
		rig.displacedInto(tester, podUpdateReplacementPod)
		rig.rt.ScriptPodDrift(rig.tokens[implementer])
		rig.rt.ScriptPodDrift(rig.tokens[tester])
	case endRecreatedRefusing:
		rig.goneRefused(refusals, tester, podUpdateReplacementPod, true)
		rig.rt.ScriptPodDrift(rig.tokens[implementer])
		rig.rt.ScriptPodDrift(rig.tokens[tester])
	default:
		t.Fatalf("end %s is not one this test brings about", end)
	}
	rig.awaitSwept(implementer, "its round to end", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	rig.settleAll()
	return rig
}
