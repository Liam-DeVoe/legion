package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// R6 of dispatch://INFRA-3271: no release deletes a disk that no read has shown clean. The release
// of a pod no claim owns and the close of a left pod with no live claim take a pod in which no role
// process runs, so no role's shim can read its workspace. The pod's launcher, which runs in the pod
// whatever its roles do, makes the read instead, with R5's count (runtime.PodWorkspaceReader). A
// read that finds work that origin does not hold, or that cannot be made, keeps the pod, and the
// issue's architect is told once for the pod, with the state of each claim and what the read found.

// workspaceKeptNotice is the notice kind that tells the issue's architect that a pod no role runs
// in is kept for its disk (record.WorkspaceKeptNotice).
const workspaceKeptNotice record.NoticeKind = "workspace-kept"

// unownedWorkspaceUnread is the hold of a pod no role runs in whose launcher could not read its
// workspace (workflow.IdleHoldUnownedWorkspaceUnread).
const unownedWorkspaceUnread = "unowned_workspace_unread"

// noRoleRig is newLeftPodRig's child with no role process in its pod: its planner failed and
// records no process, and its implementer is suspended by an earlier idle release, with no
// process either. With parked false the child stands in the workflow at implementing, which is the
// pod the release of a pod no claim owns decides (workflow.UnownedPodRelease); with parked true it
// stands where a park leaves it under its running tree, which is the left pod's close
// (workflow.LeftPodClose). The tree root's architect holds its role, so a notice of the child
// reaches that architect's role topic, which publisher records. No decline of the child's release
// stands, so nothing on record says what the child's disk holds.
func noRoleRig(t *testing.T, parked bool) (*idleRig, *outboxPublisher) {
	t.Helper()
	ctx := context.Background()
	rig := newLeftPodRig(t, parked)
	publisher := &outboxPublisher{}
	rig.runner.notices = publisher
	if _, err := rig.pool.Exec(ctx, `update claims set state = 'failed', suspended_by = '', locator = null
		where token = $1`, string(rig.token)); err != nil {
		t.Fatalf("fail the planner: %v", err)
	}
	implementer := supervise.Claim{Token: mustClaimToken(t, rig.issue.Key, claim.RoleImplementer), Project: "legion",
		Tree: rig.issue.Tree, Issue: rig.issue.Key, Role: claim.RoleImplementer, TreeEpoch: 1, Generation: 1,
		ServingGeneration: 1, State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByReleased,
		Session: "ses-implementer", SessionFile: "/tmp/implementer.jsonl", PluginContract: api.DaemonAPIVersion}
	if err := rig.st.PutClaim(ctx, implementer); err != nil {
		t.Fatalf("seed the child's released implementer: %v", err)
	}
	quietPod(t, rig)
	if _, _, err := rig.sup.Create(ctx, supervise.Claim{Token: mustClaimToken(t, rig.issue.Tree, claim.RoleArchitect),
		Project: "legion", Tree: rig.issue.Tree, Issue: rig.issue.Tree, Role: claim.RoleArchitect,
		State: supervise.StateWorking}, ""); err != nil {
		t.Fatalf("create the root's architect: %v", err)
	}
	if rig.declined(t) {
		t.Fatal("a decline of the child's release stands before any pass")
	}
	return rig, publisher
}

// launcherReads is how many workspace reads of the rig's issue the runtime was asked to make
// through the pod's launcher, and roleReads how many through a role's shim.
func launcherReads(rig *idleRig) int { return len(rig.rt.CallsOf("PodWorkspaceWork")) }

func roleReads(rig *idleRig) int { return len(rig.rt.CallsOf("WorkspaceWork")) }

// keptNotices is every notice of the kind workspaceKeptNotice published to the role topic of the
// rig's tree root's architect, in order.
func keptNotices(t *testing.T, rig *idleRig, publisher *outboxPublisher) []record.Notice {
	t.Helper()
	topic := architectTopic(t, rig.issue.Tree)
	var notices []record.Notice
	for _, published := range publisher.publishes() {
		notice, ok := published.payload.(record.Notice)
		if published.topic == topic && ok && notice.Kind == workspaceKeptNotice {
			notices = append(notices, notice)
		}
	}
	return notices
}

// holdsNamed is every hold record of the rig's project under the recorded name reason.
func holdsNamed(t *testing.T, rig *idleRig, reason string) []store.PodHold {
	t.Helper()
	var held []store.PodHold
	for _, hold := range rig.records(t).Holds {
		if hold.Reason == reason {
			held = append(held, hold)
		}
	}
	return held
}

// keptOverPasses runs pass and a drain of the outbox idleRigPasses times, and holds that the pod
// was kept through all of them: no teardown asked, the issue's Sandbox still held, the workspace
// read once through the pod's launcher and never through a role, one hold streak of hold that the
// passes counted on, and one notice to the issue's architect, which names each claim's state and
// holds every string of found. It answers that notice.
func keptOverPasses(t *testing.T, rig *idleRig, publisher *outboxPublisher, pass func(), hold string, found ...string) record.Notice {
	t.Helper()
	for range idleRigPasses {
		pass()
		rig.drain(t)
	}
	if asked := rig.rt.teardownsAsked(); len(asked) != 0 {
		t.Fatalf("teardowns asked = %v, want none: no read showed the pod's disk clean", asked)
	}
	if _, present := rig.rt.IssueSandbox(rig.issue.Key); !present {
		t.Fatal("the runtime no longer holds the issue's Sandbox")
	}
	if launcher, role := launcherReads(rig), roleReads(rig); launcher != 1 || role != 0 {
		t.Fatalf("workspace reads = %d through the pod's launcher and %d through a role over %d passes, want one through the launcher and none through a role: no role process runs in the pod",
			launcher, role, idleRigPasses)
	}
	if held := holdsNamed(t, rig, hold); len(held) != 1 || held[0].Passes < 2 {
		t.Fatalf("holds = %+v, want one %s streak the passes counted on", rig.records(t).Holds, hold)
	}
	if !rig.declined(t) {
		t.Fatal("the read that kept the pod recorded no decline")
	}
	notices := keptNotices(t, rig, publisher)
	if len(notices) != 1 {
		t.Fatalf("%s notices to the architect of %s = %d, want one for the kept pod", workspaceKeptNotice, rig.issue.Tree, len(notices))
	}
	for _, want := range append([]string{"planner=failed", "implementer=suspended"}, found...) {
		if !strings.Contains(notices[0].Reason, want) {
			t.Fatalf("the notice says %q, want it to name %q", notices[0].Reason, want)
		}
	}
	return notices[0]
}

// The release of a pod no claim owns: the child stands in the workflow, every claim of it has
// failed or has no process, and no decline says what its disk holds.
//
// one_unpushed_commit: the workspace holds one commit that origin does not hold. Over the passes
// the pod is not deleted, the launcher reads the workspace once, the pod is held as
// unowned_unpushed_work, and the architect gets one notice naming the commit.
//
// read_cannot_be_made: the launcher cannot read the workspace. The pod is kept the same way, held
// as unowned_workspace_unread, and the architect gets one notice naming the failure.
//
// control:_a_clean_read_releases: the launcher reads nothing that origin does not hold, and the
// pass's release takes the pod: the teardown is asked once, after that read.
//
// control:_a_kept_volume_needs_no_read: the issue's Sandbox keeps a volume the issue's next pod
// mounts, so the release deletes no disk and reads nothing first.
func TestTheReleaseOfAPodNoClaimOwnsReadsItsDiskFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	unownedPass := func(rig *idleRig) func() {
		return func() { rig.w.releaseUnownedPods(ctx) }
	}

	t.Run("one_unpushed_commit", func(t *testing.T) {
		rig, publisher := noRoleRig(t, false)
		rig.rt.ScriptWorkspaceWork(rig.issue.Key, idleRigUnpushed)
		keptOverPasses(t, rig, publisher, unownedPass(rig), workflow.IdleHoldUnownedUnpushedWork.String(), idleRigUnpushed.Commits[0])
		if rig.marked(t) {
			t.Fatal("the declined release left its mark behind")
		}
	})

	t.Run("read_cannot_be_made", func(t *testing.T) {
		rig, publisher := noRoleRig(t, false)
		rig.rt.FailWorkspaceWork(rig.issue.Key, errors.New("no launcher of the pod reads a workspace"))
		keptOverPasses(t, rig, publisher, unownedPass(rig), unownedWorkspaceUnread, "no launcher of the pod reads a workspace")
	})

	t.Run("control:_a_clean_read_releases", func(t *testing.T) {
		rig, publisher := noRoleRig(t, false)
		rig.rt.ScriptWorkspaceWork(rig.issue.Key, runtime.WorkspaceWork{Head: idleRigUnpushed.Head})
		rig.w.releaseUnownedPods(ctx)
		rig.drain(t)
		if asked := rig.rt.teardownsAsked(); !slices.Equal(asked, []string{"ReleaseIssue"}) {
			t.Fatalf("teardowns asked = %v, want the release of the pod no claim owns", asked)
		}
		if launcher, role := launcherReads(rig), roleReads(rig); launcher != 1 || role != 0 {
			t.Fatalf("workspace reads = %d through the pod's launcher and %d through a role, want one through the launcher before the teardown", launcher, role)
		}
		if notices := keptNotices(t, rig, publisher); len(notices) != 0 {
			t.Fatalf("notices to the architect = %+v, want none: the pod was not kept", notices)
		}
	})

	t.Run("control:_a_kept_volume_needs_no_read", func(t *testing.T) {
		rig, _ := noRoleRig(t, false)
		rig.rt.ScriptKeptVolume(rig.issue.Key, time.Now().Add(-time.Hour))
		rig.rt.ScriptWorkspaceWork(rig.issue.Key, idleRigUnpushed)
		rig.w.releaseUnownedPods(ctx)
		rig.drain(t)
		if asked := rig.rt.teardownsAsked(); !slices.Equal(asked, []string{"ReleaseIssue"}) {
			t.Fatalf("teardowns asked = %v, want the release, which keeps the Sandbox and its volume", asked)
		}
		if launcher := launcherReads(rig); launcher != 0 {
			t.Fatalf("workspace reads through the launcher = %d, want none: a kept volume the next pod mounts is not deleted", launcher)
		}
	})
}

// The close of a left pod with no live claim while its tree runs on: the child was parked under
// its running root, every claim of it has failed or has no process, and no decline says what its
// disk holds. The close deletes the Sandbox with its pod and any volume, so it always reads first.
//
// one_unpushed_commit and read_cannot_be_made: as for the release of a pod no claim owns, the pod
// is kept over the passes and the architect gets one notice.
//
// control:_a_clean_read_closes: the launcher reads nothing that origin does not hold, and the
// close goes on to the Sandbox suspension.
func TestTheCloseOfALeftPodWithNoLiveClaimReadsItsDiskFirst(t *testing.T) {
	t.Parallel()
	leftPass := func(rig *idleRig) func() { return func() { idlePasses(rig) } }

	t.Run("one_unpushed_commit", func(t *testing.T) {
		rig, publisher := noRoleRig(t, true)
		rig.rt.ScriptWorkspaceWork(rig.issue.Key, idleRigUnpushed)
		keptOverPasses(t, rig, publisher, leftPass(rig), workflow.IdleHoldUnownedUnpushedWork.String(), idleRigUnpushed.Commits[0])
		for _, left := range rig.rows(t) {
			if left == "issue_suspend" {
				t.Fatalf("rows left after the passes = %v, want no Sandbox suspension queued", rig.rows(t))
			}
		}
	})

	t.Run("read_cannot_be_made", func(t *testing.T) {
		rig, publisher := noRoleRig(t, true)
		rig.rt.FailWorkspaceWork(rig.issue.Key, errors.New("no launcher of the pod reads a workspace"))
		keptOverPasses(t, rig, publisher, leftPass(rig), unownedWorkspaceUnread, "no launcher of the pod reads a workspace")
	})

	t.Run("control:_a_clean_read_closes", func(t *testing.T) {
		rig, publisher := noRoleRig(t, true)
		idlePasses(rig)
		rig.drain(t)
		if asked := rig.rt.teardownsAsked(); !slices.Equal(asked, []string{"SuspendIssue"}) {
			t.Fatalf("teardowns asked = %v, want the close's Sandbox suspension", asked)
		}
		if launcher, role := launcherReads(rig), roleReads(rig); launcher != 1 || role != 0 {
			t.Fatalf("workspace reads = %d through the pod's launcher and %d through a role, want one through the launcher before the close", launcher, role)
		}
		if notices := keptNotices(t, rig, publisher); len(notices) != 0 {
			t.Fatalf("notices to the architect = %+v, want none: the pod was not kept", notices)
		}
	})
}
