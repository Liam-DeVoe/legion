package daemon

// The copy each turn's end owes (R1 of dispatch://INFRA-1117) as the daemon takes it: the copy is
// handed the claim whose turn ended and asks that role, the copy owed behind one in flight asks
// that role too, and a stop held for the turn's end waits for whichever copy holds that end. These
// cases drive the copy rig of workspace_copies_test.go.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// storedClaim is the rig's implementer as the store holds it, which a test hands the copy of a
// turn's end where the store records a process the machine's own claim does not (setLocatorPod
// writes the store alone).
func (r *copyRig) storedClaim(t *testing.T) supervise.Claim {
	t.Helper()
	claims, err := r.st.Claims(context.Background())
	if err != nil {
		t.Fatalf("read the claims: %v", err)
	}
	for _, c := range claims {
		if c.Token == r.token {
			return c
		}
	}
	t.Fatalf("the store holds no claim %s", r.token)
	return supervise.Claim{}
}

// liveArchitect records an architect of the rig's issue as the store holds a live role: idle, on
// a process of its own. Its token sorts before the implementer's, so a copy that chooses its role
// from the claims (runningCopyClaim) asks it, and nothing of it waits for any copy.
func (r *copyRig) liveArchitect(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	token := mustClaimToken(t, r.issue.Key, claim.RoleArchitect)
	loc, err := r.rt.Spawn(ctx, runtime.SpawnSpec{Claim: token, Role: claim.RoleArchitect})
	if err != nil {
		t.Fatalf("mint the architect's locator: %v", err)
	}
	stored := supervise.Claim{Token: token, Project: "legion", Tree: r.issue.Tree, Issue: r.issue.Key,
		Role: claim.RoleArchitect, TreeEpoch: 1, Generation: 1, ServingGeneration: 1,
		State: supervise.StateIdle, Locator: &loc, Session: "ses-architect", SessionFile: "/tmp/architect.jsonl",
		PluginContract: api.DaemonAPIVersion}
	if err := r.st.PutClaim(ctx, stored); err != nil {
		t.Fatalf("seed the live architect: %v", err)
	}
}

// holdStop holds a daemon's suspension of the implementer for the end of the turn it is in.
func (r *copyRig) holdStop(t *testing.T) {
	t.Helper()
	if err := r.machine.Handle(context.Background(), supervise.RequestSuspend{Claim: r.token, By: supervise.SuspendedByDaemon,
		Reason: "LEGION-208 left implementing"}); !errors.Is(err, supervise.ErrSuspendHeld) {
		t.Fatalf("the suspension of the implementer in a turn = %v, want it held for the turn's end", err)
	}
}

// waitsForTheCopy fails unless the implementer is idle, holding its stop, with its process not
// stopped: its held stop is waiting for a copy.
func (r *copyRig) waitsForTheCopy(t *testing.T) {
	t.Helper()
	if testwait.Holds(time.Second, testwait.Poll, func() bool { return len(r.rt.CallsOf("Suspend")) > 0 }) {
		t.Fatalf("the implementer's process was stopped while the copy of its turn's end was still being built; the daemon logged:\n%s", r.logs.String())
	}
	if c := r.machine.Claim(); c.State != supervise.StateIdle || !c.SuspensionHeld {
		t.Fatalf("the implementer is %s (held %v) while the copy of its turn's end is built, want idle holding its stop", c.State, c.SuspensionHeld)
	}
}

// stopsOnceCopied waits for the implementer's held stop to land, and fails unless it landed once.
func (r *copyRig) stopsOnceCopied(t *testing.T) {
	t.Helper()
	testwait.Eventually(t, "the implementer's held stop to land once the copy of its turn's end has ended", func() bool {
		return r.machine.Claim().State == supervise.StateSuspended
	})
	if stops := r.rt.CallsOf("Suspend"); len(stops) != 1 {
		t.Fatalf("the implementer's process was stopped %d times, want once", len(stops))
	}
}

// heldBundler makes the rig's bundler hold every copy until the call it answers is made, and
// then answer copyRigBundle.
func (r *copyRig) heldBundler() func() {
	release := make(chan struct{})
	r.bundler.holds(func(ctx context.Context) (runtime.WorkspaceBundle, error) {
		select {
		case <-release:
			return copyRigBundle, nil
		case <-ctx.Done():
			return runtime.WorkspaceBundle{}, ctx.Err()
		}
	})
	return func() { close(release) }
}

// The copy a turn's end takes asks the role whose turn ended, as the machine handed it the claim
// at that end, and reads no claims for a role of its own. The issue's other live role, first by
// token, is what a read of the claims would choose, and nothing of that role waits for the copy:
// its own stop, a pause's of an idle role above all, lands whenever it comes and cuts off a copy
// asked of it, and the turn's end is then in no copy. The stop held for the turn's end does wait:
// while the bundler is still building the copy, the role whose turn ended is idle and running,
// and once the copy has ended the stop lands.
//
// The control is the copy reading the claims itself again, as it did before it was handed the
// claim: it then asks the architect, and the test fails at the role the bundle was asked of.
func TestATurnsEndCopyAsksTheRoleWhoseTurnEndedAndTheStopHeldForThatEndWaitsForIt(t *testing.T) {
	rig := newCopyRig(t)
	rig.copiesTurnEnds()
	rig.liveArchitect(t)
	release := rig.heldBundler()
	rig.turnStart(t)
	rig.holdStop(t)

	rig.turnEnd(t)

	rig.awaitAsks(t, 1)
	if asked := rig.bundler.reads(); asked[0] != claim.RoleImplementer {
		t.Fatalf("the copy of the implementer's turn's end was asked of %v, want the implementer whose turn ended", asked)
	}
	rig.waitsForTheCopy(t)

	release()
	rig.awaitCopy(t, len(copyRigBundle.Bundle))
	rig.stopsOnceCopied(t)
	if asked := rig.bundler.reads(); len(asked) != 1 {
		t.Fatalf("the bundler was asked %v, want the one copy of the turn's end", asked)
	}
}

// A turn that ends while the cadence's copy of its issue is still being built is owed a copy
// behind it, and that owed copy asks the role whose turn ended, as the turn's own copy would have:
// it is handed the claim the turn's end handed its copy, and reads no claims to find a role. The
// stop held for that turn's end waits for the owed copy, which is the one that holds the end: the
// copy in flight read the workspace before the turn's last work was written. So the role whose
// turn ended runs on while the copy in flight is built, and its stop lands once the owed copy has
// ended. The cadence asked the issue's first live role by token, the architect.
//
// The control is the owed copy reading the claims itself again, as it did before it carried the
// role it was owed with: it then asks the architect a second time, and the test fails at the role
// the owed copy was asked of.
func TestTheCopyOwedToATurnsEndAsksTheRoleWhoseTurnEndedAndItsHeldStopWaitsForIt(t *testing.T) {
	rig := newCopyRig(t)
	rig.copiesTurnEnds()
	rig.liveArchitect(t)
	ctx := context.Background()
	release := rig.heldBundler()
	rig.turnStart(t)
	rig.w.copyWorkingWorkspaces(ctx)
	rig.awaitAsks(t, 1)
	rig.holdStop(t)

	rig.turnEnd(t)

	if !testwait.Holds(testwait.Bounded(t, 30*time.Second, time.Second), testwait.Poll, func() bool {
		rig.w.copies.mu.Lock()
		defer rig.w.copies.mu.Unlock()
		return rig.w.copies.issues[rig.issue.Key].owed != nil
	}) {
		t.Fatalf("the turn's end was never owed behind the copy in flight; the daemon logged:\n%s", rig.logs.String())
	}
	rig.waitsForTheCopy(t)

	release()
	rig.awaitAsks(t, 2)
	if asked := rig.bundler.reads(); asked[0] != claim.RoleArchitect || asked[1] != claim.RoleImplementer {
		t.Fatalf("the bundler was asked %v, want the cadence's copy of the architect and the owed copy of the implementer whose turn ended", asked)
	}
	rig.stopsOnceCopied(t)
	rig.awaitQuiet(t)
	if asked := rig.bundler.reads(); len(asked) != 2 {
		t.Fatalf("the bundler was asked %v, want the copy in flight and the one the turn's end owed after it", asked)
	}
}
