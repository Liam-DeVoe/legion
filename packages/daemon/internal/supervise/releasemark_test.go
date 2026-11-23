package supervise

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
)

// An idle release saves the unpushed work of the issue's workspace as a bundle, and the issue's
// next pod restores it before any role starts, so the first launch of a role the release
// suspended, an agent of which had run, puts the pod it goes into in the role's set
// (addRestoringPod). The release's mark follows the bundle, not the suspension's source
// (INFRA-1484's architect, 2026-11-23T04:05Z): a later suspension of the claim before the bundle is
// restored keeps the role the release's, whatever its source, so the claim's next launch still
// puts its pod in the set. A source that cleared the mark would hide the loss the pause's stop
// once hid: the role's work is on the restored pod's disk, and no end of a round over that pod
// would name it.
//
// Each row suspends a released claim again before its restore, by the daemon (a linger, a park,
// a close or a hand-over taking the claim over) or by the operator, then resumes it as that source
// is resumed, into the next pod. A row of a takeover suspends the claim while it stands released;
// a row of a launch suspends the woken first launch while it waits in the wake pacing, before it
// reaches any pod.
//
// Red at 1640db51a4 (tester, INFRA-4425): the takeover (suspendedAgain) and the suspension of
// the waiting launch (suspended) clear the mark, and markRestoringLaunch reads only a claim that
// stands released, so in every row the next pod is missing from the set.
func TestALaterSuspensionBeforeTheRestoreKeepsTheRoleTheReleases(t *testing.T) {
	released := func(t *testing.T, h *harness) {
		t.Helper()
		h.inPod(refusedPod)
		h.must(RequestSuspend{Claim: h.token, Reason: "the idle pod was released", By: SuspendedByReleased})
		if c := h.claim(); !c.Released() {
			t.Fatalf("the claim is %s by %q, want it released", c.State, c.SuspendedBy)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once released,")
	}
	resumedInto := func(t *testing.T, h *harness, by SuspendSource, pod string) {
		t.Helper()
		h.rt.ScriptResume(fake.SpawnResult{Locator: podProcess(pod, 3)})
		if by == SuspendedByOperator {
			h.must(RequestResume{Claim: h.token})
		} else {
			h.must(RequestRuleResume{Claim: h.token, Reason: "it owns its phase"})
		}
		// A resume the wake pacing holds launches once its turn comes, well inside the boot
		// timeout; one it does not hold has launched already.
		for i := 0; i < 4 && h.claim().Locator == nil; i++ {
			h.advance(wakeInterval + wakePart)
		}
		if c := h.claim(); c.State != StateLaunching || locatorPod(c.Locator) != pod {
			t.Fatalf("the resumed claim is %s at %+v, want it launching in pod %s", c.State, c.Locator, pod)
		}
	}

	for _, by := range []SuspendSource{SuspendedByDaemon, SuspendedByOperator} {
		t.Run("a suspension by the "+string(by)+" that takes the released claim over", func(t *testing.T) {
			h := newHarness(t)
			released(t, h)
			h.must(RequestSuspend{Claim: h.token, Reason: "the claim is taken over", By: by})
			if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != by {
				t.Fatalf("the claim is %s by %q, want it suspended by the %s", c.State, c.SuspendedBy, by)
			}
			resumedInto(t, h, by, replacedPod)
			h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}},
				"once the "+string(by)+"'s resume came up in the pod that restored the release's bundle, before its agent registered,")
		})

		t.Run("a suspension by the "+string(by)+" of the woken first launch before any pod", func(t *testing.T) {
			h := pacedHarness(t, wakeRate)
			released(t, h)
			h.must(RequestRuleResume{Claim: h.token, Reason: "a wake for a delivery"})
			h.wantWaitingWake(WakeLaunch)
			if c := h.claim(); c.Locator != nil {
				t.Fatalf("the woken claim records %+v, want its launch waiting its turn before any pod", c.Locator)
			}
			h.must(RequestSuspend{Claim: h.token, Reason: "the claim is taken over", By: by})
			if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != by || c.Locator != nil {
				t.Fatalf("the claim is %s by %q at %+v, want it suspended by the %s before any pod", c.State, c.SuspendedBy, c.Locator, by)
			}
			h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once its woken launch was suspended before any pod,")
			resumedInto(t, h, by, replacedPod)
			h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}},
				"once the "+string(by)+"'s resume came up in the pod that restored the release's bundle, before its agent registered,")
		})
	}
}
