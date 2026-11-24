package daemon

// The store's record of the pods that took an idle release's work (store.PodTookWorkspaceBundle),
// which every end of an update round over such a pod reads (workOnRoundPod, roundPodTookRelease):
// it survives a daemon restart between the take and the round's end, and it stands until the
// issue's work is over, when the outbox's dropReleasedWork deletes it with the issue's bundle.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The daemon restarts between the take that sent P an idle release's work and the end of the
// round over P, and the end still names each role whose work the release saved. The tester and
// the reviewer ran in P0, whose release saved the workspace they share and suspended both; the
// tester's wake brought the issue's pod back as P, which took the bundle, and the tester answers
// the round. The implementer never ran an agent before the restart: its first launch, with no
// session, is refused for P, so it is a member of the round with no session and an empty set.
// After the restart P is recreated, and the implementer's retry comes up in the pod in P's place
// and registers there.
//
// A restart reads every stored claim that shows a session as a claim whose set is unknown
// (supervise's bootRegisteredPods), so the reviewer is named whatever the store holds. The
// implementer's stored claim shows no session, so its set is known: {the pod in P's place}. It
// never ran an agent before the release, so none of its work is in the bundle. It is named only
// because the rule names every member with a session or a pod at an end over a pod that took an
// idle release's work, which may name a role whose work that pod does not hold: the cost
// INFRA-1484's architect accepted
// (dispatch://INFRA-4425/comment/566feff4-2b27-41f2-89a9-b1a333d97ecd). That is why it is the one
// member whose naming after the restart shows that the store's record of the take survived it:
// the restarted daemon reads that record at the end, and nothing else names the implementer. The
// test also reads the record through the restarted daemon's own read, before the end.
func TestARestartBetweenTheTakeAndTheRoundsEndStillNamesTheRoles(t *testing.T) {
	implementer, tester, reviewer := claim.RoleImplementer, claim.RoleTester, claim.RoleReviewer
	rig := newPodUpdateRig(t, podUpdateRole{role: implementer, state: supervise.StateQueued},
		podUpdateRole{role: tester, state: supervise.StateQueued}, podUpdateRole{role: reviewer, state: supervise.StateQueued})
	refusals := rig.refuseLaunches()
	rig.startInPod(tester, releasedPod)
	rig.startInPod(reviewer, releasedPod)
	rig.releases(releasedPod, 1, releasedSavedAt, tester, reviewer)
	rig.wakesInto(tester, podUpdatePod, roundPodMadeAt, nil)
	refusals.refuse(implementer, podUpdatePod)
	rig.refusedFirstLaunch(implementer)
	rig.drifts(podUpdateExchangeDrift)

	rig.tick()
	rig.awaitRound(implementer, podUpdateRound)
	rig.awaitRound(reviewer, podUpdateRound)
	rig.awaitAsked(tester, podUpdateRound)
	rig.signal(tester, "c0ffee")

	rig, refusals = rig.restarted()
	// The restarted daemon's read answers true on a failed read too, so the store is read itself.
	if took, err := rig.st.PodTookWorkspaceBundle(rig.ctx, podUpdateIssue, podUpdatePod); err != nil || !took {
		t.Fatalf("after the restart the store reads that pod %s took a release's work: %t (%v), want the record of the take before the restart", podUpdatePod, took, err)
	}
	if !rig.s.roundPodTookRelease(rig.ctx, podUpdateIssue, podUpdateRound) {
		t.Fatalf("the restarted daemon reads that pod %s took no release's work, want the store's record of the take before the restart", podUpdatePod)
	}
	if c := rig.claimOf(implementer); c.Session != "" || c.RegisteredPods.Unknown || len(c.RegisteredPods.Pods) != 0 {
		t.Fatalf("after the restart the implementer has session %q and set %+v, want no session and a known, empty set", c.Session, c.RegisteredPods)
	}
	refusals.refuse(implementer, podUpdatePod)
	rig.refusedFirstLaunch(implementer)

	rig.displacedInto(tester, podUpdateReplacementPod)
	if pod := rig.retriesInto(refusals, implementer, podUpdateReplacementPod); pod != podUpdateReplacementPod {
		t.Fatalf("the implementer's retry came up in pod %s, want the pod in P's place, %s", pod, podUpdateReplacementPod)
	}
	rig.registerInto(implementer, podUpdateReplacementPod)
	for _, role := range []claim.Role{implementer, tester} {
		rig.rt.ScriptPodDrift(rig.tokens[role])
	}
	rig.awaitSwept(implementer, "its round to end", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	rig.settleAll()

	lost := loggedLines(t, rig.logs.String(), podWentLostLine)
	if len(lost) != 1 {
		t.Fatalf("the round's end was logged %+v, want one line that the members that had not answered lost what they had not pushed", lost)
	}
	wantField(t, lost[0], "lost", implementer, reviewer)
	messages := rig.messages()
	if len(messages) != 1 {
		t.Fatalf("the issue was written %q, want one message that its pod went", messages)
	}
	for _, want := range []string{"The pod that ran this issue's roles is gone", "implementer (ready)", "reviewer (suspended)"} {
		if !strings.Contains(messages[0], want) {
			t.Errorf("the message does not say %q:\n%s", want, messages[0])
		}
	}
	if strings.Contains(messages[0], "tester (") {
		t.Errorf("the message names the tester, which answered the round:\n%s", messages[0])
	}
}

// The record that a pod took an idle release's work stands until the issue's work is over. A
// park of a role of the issue (a `suspend` row, which takes the role over) keeps it, as it keeps
// the issue's bundle, since the issue comes back; the issue's close deletes both
// (dropReleasedWork). The confirmed restore before them deletes the bundle's own row and leaves
// the record, which is why the record is a table of its own.
func TestThePodsThatTookAReleasesWorkGoWithTheIssuesWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner, sup, _ := hookRunner(t, &outboxPublisher{})
	st, ok := runner.trees.(*store.Store)
	if !ok {
		t.Fatalf("the runner's tree store is %T, want the store", runner.trees)
	}
	releasedClaim(t, sup, claim.RoleImplementer)
	releasedClaim(t, sup, claim.RoleArchitect)
	const sourcePod, takingPod = "pod-the-release-took", "pod-that-took-the-work"
	at := time.Now().Add(-time.Hour)
	if err := st.PutWorkspaceBundle(ctx, pauseIssue, 7, runtime.WorkspaceBundle{Bundle: []byte("PACK the released work"),
		Head: passedOverHead, At: at, PodUID: sourcePod}); err != nil {
		t.Fatalf("save the release's bundle: %v", err)
	}
	saved, take, err := st.TakeWorkspaceBundleForRestore(ctx, "legion", pauseIssue, takingPod, at.Add(time.Minute))
	if err != nil || take != runtime.WorkspaceBundleSend {
		t.Fatalf("the pod after the release was answered %v (%v), want the bundle sent", take, err)
	}
	took := func(when string, want bool) {
		t.Helper()
		got, err := st.PodTookWorkspaceBundle(ctx, pauseIssue, takingPod)
		if err != nil || got != want {
			t.Fatalf("%s the store reads that pod %s took the release's work: %t (%v), want %t", when, takingPod, got, err, want)
		}
	}
	took("after the take,", true)
	if err := st.DeleteRestoredWorkspaceBundle(ctx, pauseIssue, saved.Release); err != nil {
		t.Fatalf("confirm the restore: %v", err)
	}
	took("after the confirmed restore,", true)

	if err := runner.execute(ctx, mustOutboxRow(t, pauseIssue, record.SuperviseRequest{Op: "suspend", Tree: pauseIssue,
		Role: claim.RoleArchitect, Generation: 1, Reason: "the tree was parked"}, time.Now())); err != nil {
		t.Fatalf("the architect's suspend: %v", err)
	}
	took("after a park of a role,", true)

	if err := runner.execute(ctx, mustOutboxRow(t, pauseIssue, record.SuperviseRequest{Op: "issue_close", Tree: pauseIssue,
		Role: claim.RoleImplementer, Generation: 1}, time.Now())); err != nil {
		t.Fatalf("the implementer's issue close: %v", err)
	}
	took("after the issue's close,", false)
}
