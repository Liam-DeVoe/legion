package daemon

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
)

// bundleAsks is the rig's fake runtime with the save of a workspace that the Kubernetes runtime
// has (runtime.WorkspaceBundler): it records each ask by role and answers it through the role's own
// connection, as the Sandbox runtime asks the role's shim (sandbox's Runtime.BundleWorkspace).
type bundleAsks struct {
	*fake.Runtime
	conns *fake.Conns
	mu    sync.Mutex
	asked []claim.Role
}

func (b *bundleAsks) BundleWorkspace(ctx context.Context, issue string, role claim.Role) (runtime.WorkspaceBundle, error) {
	b.mu.Lock()
	b.asked = append(b.asked, role)
	b.mu.Unlock()
	token, err := claim.NewToken(podUpdateProject, issue, role)
	if err != nil {
		return runtime.WorkspaceBundle{}, err
	}
	conn, ok := b.conns.Conn(token)
	if !ok {
		return runtime.WorkspaceBundle{}, fmt.Errorf("no connection of %s to ask for its bundle", token)
	}
	return conn.BundleWorkspace(ctx, issue, runtime.WorkspaceBundleTimeout)
}

// roles is each role a save was asked of, in order.
func (b *bundleAsks) roles() []claim.Role {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.asked)
}

// An update round saves nothing of the pod's disk: once every role answered it, the round replaces
// the pod with no ask of a save, of the runtime or of any role's shim, though the runtime can save
// a workspace. So what a role did not push goes with the pod, beside a running fix as anywhere: the
// shared working copy's commits and the fix's working copy's, a step's commits that its report
// names among them. The update message says so to each role (supervise.UpdateTask).
//
// The control is the idle release, the one path that saves the disk before the pod goes: over the
// same kind of runtime it asks the pod's live role for its workspace's bundle before it takes the
// pod down.
func TestAnUpdateRoundAsksNoShimForABundle(t *testing.T) {
	t.Parallel()

	t.Run("the update round's replacement of the pod", func(t *testing.T) {
		t.Parallel()
		var asks *bundleAsks
		roles := []claim.Role{claim.RoleImplementer, claim.RoleTester}
		rig := newPodUpdateRigWith(t, podUpdateRigOptions{over: func(rt *fake.Runtime) runtime.Runtime {
			asks = &bundleAsks{Runtime: rt}
			return asks
		}}, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester))
		asks.conns = rig.conns
		for _, role := range roles {
			rig.workers[role].SetWorkspaceBundle(runtime.WorkspaceBundle{Bundle: []byte("PACK"), Head: "c0de000000000000000000000000000000000001"})
		}
		rig.drifts(podUpdateDrift)
		rig.tick()
		for _, role := range roles {
			rig.awaitAsked(role, podUpdateRound)
		}
		for _, role := range roles {
			rig.signal(role, "a1b2c3d")
		}
		rig.awaitReplacement(1, "once every role answered the round")

		if asked := asks.roles(); len(asked) != 0 {
			t.Errorf("the round asked the runtime for a save of %v, want none: an update round saves nothing of the disk", asked)
		}
		for _, role := range roles {
			if asked := rig.workers[role].Bundlings(); len(asked) != 0 {
				t.Errorf("the round asked the %s's shim for its workspace's bundle %d time(s), want none", role, len(asked))
			}
		}
	})

	t.Run("control: the idle release asks the pod's live role for its bundle", func(t *testing.T) {
		t.Parallel()
		rig := newIdleRig(t)
		rig.queuedBy(t, func() { rig.w.releaseIdlePods(context.Background()) })
		rig.drain(t)
		if bundled, _ := rig.rt.reads(); !slices.Equal(bundled, []claim.Role{claim.RoleImplementer}) {
			t.Fatalf("the idle release asked for a save of %v, want the pod's one live role, the implementer", bundled)
		}
	})
}
