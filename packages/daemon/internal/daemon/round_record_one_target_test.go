package daemon

// The Legion reviewer's Important 1 on #5285 at 7da847c0: one replacement can leave two records,
// each under an operation identity of its own, on one target (what the act takes and the uid of
// what it takes). The runtime reads an act made from its target alone, so where two records of one
// target stand, each reads as made, and each is finished with a notice of its own: the issue's
// architect is told twice of one replacement, and the late notice, built once the round is over,
// can say what is false of a kept volume or of the disk. One target is taken once, so the finish
// of an act that took it finishes every record of that target, with one notice. The cases are the
// reviewer's three traces:
//
//   - a: a replacement by issue has written its record and waits for the pod's launch turn, which
//     a launch holds. The launch replaces the round's pod as the round's own replacement, with a
//     record of its own, and the replacement by issue then finds another pod and asks no last
//     word. Its own record is forgotten, and the round ends as the launch's, told once, from the
//     read the launcher made at the launch.
//   - b: a launch whose reading of the round answered that the round's pod is replaced fails
//     before its suspension lands. Its record stands while the daemon remembers the launch. The
//     round's replacement by issue then takes the pod under a record of its own, and its notice is
//     the one notice.
//   - c: a launch that deletes the issue's Sandbox and the kept volume it owns stops between its
//     suspension and the delete, so its record stands uncertain. The next launch records its own
//     delete of the same Sandbox, deletes it and tells the architect, and the first record tells
//     nobody again.

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// launchReadCommit is the commit the launcher's read at the launch finds in trace a, other than the
// commit the read before the replacement by issue found (passedOverCommit).
const launchReadCommit = "5a7c9e1b3d5f7a9c8c2e4a6b0d1f3e5a7c9b1d3f"

// podlessBreakingRound is a rig whose implementer the daemon suspended with the round over a drift
// that breaks the pod recorded, unasked, with no process in the pod, over a workspace that holds
// passedOverCommit: the round is over, and nothing holds its pod.
func podlessBreakingRound(t *testing.T) *podUpdateRig {
	t.Helper()
	rig := newPodUpdateRig(t, podUpdateRole{role: claim.RoleImplementer, state: supervise.StateSuspended, session: true,
		contract: api.DaemonAPIVersion, suspendedBy: supervise.SuspendedByDaemon, round: podUpdateRound, reasons: roundBreakingDrift, unasked: true})
	rig.drifts(roundBreakingDrift)
	rig.rt.ScriptIssuePod(podUpdateIssue, podUpdatePod)
	rig.rt.ScriptWorkspaceWork(podUpdateIssue, roundUnpushed)
	return rig
}

func TestAReplacementByIssueThatALaunchsReplacementOvertookTellsTheArchitectOnce(t *testing.T) {
	t.Parallel()
	rig := podlessBreakingRound(t)
	rig.rt.RoundPodEnds = rig.s.roundPodEndsAtLaunch
	launched := false
	rig.rt.HoldReplaceIssuePod(func() {
		if launched {
			return
		}
		launched = true
		if records := rig.replacementRecords(); records != 1 {
			t.Errorf("the store holds %d records while the replacement by issue waits for the pod's launch turn, want its own", records)
		}
		rig.rt.ScriptWorkspaceWork(podUpdateIssue, runtime.WorkspaceWork{Head: passedOverHead, Commits: []string{launchReadCommit}, Count: 1})
		rig.intoRoundPod(claim.RoleImplementer)
		rig.handle(claim.RoleImplementer, supervise.RequestResume{Claim: rig.tokens[claim.RoleImplementer]})
	})
	rig.tick()
	if !launched {
		t.Fatal("the sweep asked for no replacement by issue, so this case proves nothing")
	}
	if asked := rig.byIssueAsks(); len(asked) != 1 || asked[0].Method != "ReplaceIssuePodNoPod" {
		t.Fatalf("the replacement by issue was asked %+v, want one that found the launch's replacement in its place", asked)
	}
	if replaced := rig.rt.CallsOf("ReplaceRoundPodAtLaunch"); len(replaced) != 1 || replaced[0].PodUID != podUpdatePod {
		t.Fatalf("the launch replaced %+v, want the round's pod %s once", replaced, podUpdatePod)
	}
	pod := podOf(rig.claimOf(claim.RoleImplementer).Locator)
	if pod == "" || pod == podUpdatePod {
		t.Fatalf("the launch started the implementer in pod %q, want the pod in place of the round's %s", pod, podUpdatePod)
	}
	rig.registerInto(claim.RoleImplementer, pod)
	rig.awaitSwept(claim.RoleImplementer, "its round to end as the launch's replacement", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	rig.wantToldOnceNaming("once the launch replaced the round's pod, told from the launcher's read at the launch", launchReadCommit)
	if records := rig.replacementRecords(); records != 0 {
		t.Fatalf("the store holds %d records of the round's pod once its notice is queued, want none", records)
	}
}

func TestALaunchThatFailedBeforeItsSuspensionLeavesOneNoticeOfTheReplacement(t *testing.T) {
	t.Parallel()
	rig := podlessBreakingRound(t)
	// The launch's reading of the round answers that the round's pod is replaced: its record is
	// written, and the daemon keeps the launch in memory. The launch then fails before its
	// suspension lands, so the pod still runs.
	if ends, err := rig.s.roundPodEndsAtLaunch(rig.ctx, rig.tokens[claim.RoleImplementer], podUpdatePod); err != nil || !ends {
		t.Fatalf("the launch's reading of the round = %t, %v; want the pod replaced", ends, err)
	}
	if records := rig.replacementRecords(); records != 1 {
		t.Fatalf("the store holds %d records once the launch's reading answered, want the launch's", records)
	}
	rig.awaitSwept(claim.RoleImplementer, "its round to end with the replacement by issue", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	if replaced := rig.issueReplacements(); len(replaced) != 1 || replaced[0].PodUID != podUpdatePod {
		t.Fatalf("the pod was replaced by issue %+v, want the round's pod %s once", replaced, podUpdatePod)
	}
	rig.wantToldOnce("once the replacement by issue took the pod whose launch had failed")
	if records := rig.replacementRecords(); records != 0 {
		t.Fatalf("the store holds %d records of the round's pod once its notice is queued, want none", records)
	}
}

func TestTwoLaunchesThatDeleteOneKeptVolumeTellTheArchitectOnce(t *testing.T) {
	t.Parallel()
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
	// The first launch read the workspace through a launcher of the pod and found a commit,
	// recorded its delete of the Sandbox, suspended the pod, and stopped before the delete.
	first := volumeResetTaken(rig, "launch-reset-1", podUpdatePod)
	first.Work, first.Err = roundUnpushed, nil
	if err := rig.s.recordLaunchTakes(rig.ctx, first); err != nil {
		t.Fatalf("record the first launch's delete of the Sandbox: %v", err)
	}
	rig.rt.ScriptSuspendedOver(podUpdatePod)
	for range roundSweeps {
		rig.tick()
	}
	if records := rig.replacementRecords(); records != 1 {
		t.Fatalf("the store holds %d records while the Sandbox and its kept volume stand, want the first launch's", records)
	}

	// The next launch finds no live pod, records its own delete of the same Sandbox, deletes it,
	// and hands the daemon what it took.
	second := volumeResetTaken(rig, "launch-reset-2", "")
	if err := rig.s.recordLaunchTakes(rig.ctx, second); err != nil {
		t.Fatalf("record the second launch's delete of the Sandbox: %v", err)
	}
	rig.rt.ScriptSandboxGone(keptSandboxUID)
	rig.s.tellLaunchTook(rig.ctx, second)
	rig.wantToldOnceNaming("once the second launch deleted the Sandbox", keptSandboxUID)
	if records := rig.replacementRecords(); records != 0 {
		t.Fatalf("the store holds %d records of the Sandbox once its notice is queued, want none", records)
	}
}

// The Legion reviewer's Important 1 on #5285 at acda4d1e: a launch whose act deletes the issue's
// Sandbox and the kept volume it owns (runtime.VolumeAct), over a live pod of that Sandbox, takes
// the pod with the volume. A replacement by issue has recorded its own act on that pod and waits
// for the pod's launch turn, which the launch holds; the launch records its act, takes the pod and
// the Sandbox, and tells the architect. The replacement by issue then finds the pod taken. The
// architect is told once, by the launch, which says what was taken; the round's record of the pod
// tells nothing more.
func TestALaunchsVolumeActOverTheLivePodOfARoundsRecordTellsTheArchitectOnce(t *testing.T) {
	t.Parallel()
	rig := podlessBreakingRound(t)
	taken := runtime.TakenPod{Operation: "launch-volume-1", Claim: rig.tokens[claim.RoleImplementer], Issue: podUpdateIssue, PodUID: podUpdatePod,
		Why: "the issue's Sandbox has a disk shape that this launch does not render", Act: runtime.VolumeAct(keptSandboxUID), WithPod: true,
		Work: runtime.WorkspaceWork{Head: passedOverHead, Commits: []string{launchReadCommit}, Count: 1}}
	launched := false
	rig.rt.HoldReplaceIssuePod(func() {
		if launched {
			return
		}
		launched = true
		if err := rig.s.recordLaunchTakes(rig.ctx, taken); err != nil {
			t.Errorf("record the launch's act: %v", err)
		}
		rig.rt.ScriptSuspendedOver(podUpdatePod)
		rig.rt.ScriptSandboxGone(keptSandboxUID)
		rig.s.tellLaunchTook(rig.ctx, taken)
	})
	rig.awaitSwept(claim.RoleImplementer, "its round to end once the launch took its pod", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	if !launched {
		t.Fatal("the sweep asked for no replacement by issue, so this case proves nothing")
	}
	for range roundSweeps {
		rig.tick()
	}
	told := rig.architectNotices(record.WorkspaceTakenNotice)
	if len(told) != 1 || !strings.Contains(told[0].Reason, "deleted the Sandbox") || !strings.Contains(told[0].Reason, launchReadCommit) {
		t.Fatalf("the architect was told %+v, want one workspace-taken notice, the launch's, naming the Sandbox it deleted and the commit %s its read found",
			told, launchReadCommit)
	}
	if records := rig.replacementRecords(); records != 0 {
		t.Fatalf("the store holds %d records once the notice is queued, want none", records)
	}
}
