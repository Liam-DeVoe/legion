package sandbox

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// A launcher's hello says whether it waits for a start's credential, and the runtime answers it for
// the slot (launchers.credentialWaits): a launcher of an image before that wait answers its pod's
// uid and false, which a re-role into its slot waits out; a launcher that waits answers true; and a
// slot with no launcher connected, or one whose connection closed, answers true, since the launch
// then builds or waits for a pod of this release.
func TestASlotsLauncherSaysWhetherItWaitsForTheCredential(t *testing.T) {
	launchers := testLaunchers()
	if pod, waits := launchers.credentialWaits(workerToken); pod != "" || !waits {
		t.Fatalf("with no launcher connected = %q, %t; want none, waits", pod, waits)
	}
	old := launchers.accept(workerToken, testTree, rigNow, time.Time{}, shimwire.LauncherHello{PodUID: "pod-1", LauncherID: "old"})
	if pod, waits := launchers.credentialWaits(workerToken); pod != "pod-1" || waits {
		t.Fatalf("a launcher that said no credential wait = %q, %t; want pod-1, no wait", pod, waits)
	}
	launchers.accept(workerToken, testTree, rigNow, time.Time{}, shimwire.LauncherHello{PodUID: "pod-2", LauncherID: "new", CredentialWait: true})
	if pod, waits := launchers.credentialWaits(workerToken); pod != "pod-2" || !waits {
		t.Fatalf("a launcher that waits = %q, %t; want pod-2, waits", pod, waits)
	}
	if !old.closed {
		t.Fatal("the earlier launcher's connection is still open: the case's premise")
	}
	launchers.accept(workerToken, testTree, rigNow, time.Time{}, shimwire.LauncherHello{PodUID: "pod-3", LauncherID: "newest"}).close()
	if pod, waits := launchers.credentialWaits(workerToken); pod != "" || !waits {
		t.Fatalf("after the launcher's connection closed = %q, %t; want none, waits", pod, waits)
	}
}

// A launch into a kept pod judges the launcher it reached, whatever the outbox judged before it:
// right after a daemon restart no launcher is connected, so the outbox lets a re-role through, and
// the launch may then reach a launcher of the release before, whose hello said no credential wait
// and which starts a child at once, as whatever App the slot's gh files held. Key a's claim
// `…-implementer` runs as the implementer in the kept pod, then is launched as the tester, a role
// of the review App: with such a launcher the launch is refused, runtime.ErrAwaitsPodUpdate,
// naming the pod and the slot, before anything is written, stopped or sent in the pod. The
// controls are the same launch with a launcher that waits, and a launch as the merger, a role of
// the implement App the slot already holds, with a launcher that does not wait: both start.
func TestALaunchOfAnotherAppIntoASlotWhoseLauncherDoesNotWaitIsRefused(t *testing.T) {
	keyed := claim.Token("legion-legion-legion-208-implementer")
	for _, tc := range []struct {
		name    string
		waits   bool
		role    claim.Role
		refused bool
	}{
		{name: "a launcher that does not wait, the claim on a role of another App", role: claim.RoleTester, refused: true},
		{name: "control: a launcher that waits, the claim on a role of another App", waits: true, role: claim.RoleTester},
		{name: "control: a launcher that does not wait, the claim on a role of the same App", role: claim.RoleMerger},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var options []rigOption
			if !tc.waits {
				options = append(options, withoutCredentialWait())
			}
			g := newRig(t, nil, options...)
			g.spawn(rootSpec(t))
			loc := g.spawn(testSpec(t, keyed, claim.RoleImplementer, testTree))
			g.clearActions()
			sent := len(g.sent(keyed))

			again := testSpec(t, keyed, tc.role, testTree)
			again.Generation++
			_, err := g.r.Spawn(g.ctx, again)
			starts := slices.ContainsFunc(g.sent(keyed)[sent:], func(frame shimwire.Frame) bool {
				start, ok := frame.(shimwire.LauncherStart)
				return ok && start.Generation == again.Generation
			})
			if !tc.refused {
				if err != nil || !starts {
					t.Fatalf("the launch as the %s = %v, sending a start: %t; want it started", tc.role, err, starts)
				}
				return
			}
			if !errors.Is(err, runtime.ErrAwaitsPodUpdate) {
				t.Fatalf("the launch as the %s into a launcher that does not wait = %v, want an error that is runtime.ErrAwaitsPodUpdate", tc.role, err)
			}
			if !strings.Contains(err.Error(), loc.Sandbox.PodUID) || !strings.Contains(err.Error(), "implementer") {
				t.Errorf("the refusal %q, want it to name the pod %s and the slot implementer", err, loc.Sandbox.PodUID)
			}
			if writes := g.writes(); len(writes) > 0 {
				t.Errorf("the refused launch wrote %v, want nothing written", writes)
			}
			if frames := g.sent(keyed)[sent:]; len(frames) > 0 {
				t.Errorf("the refused launch sent its launcher %#v, want nothing", frames)
			}
		})
	}
}

// A slot whose name no claim token carries is refused, never answered as a slot whose launcher
// waits for the credential: that answer lets a re-role into the slot start with no credential wait
// (the daemon's reroleWaitsForCredential). The control: the slot of an agent's name with no
// launcher connected still answers that the launch waits.
func TestASlotNoTokenCarriesIsRefusedRatherThanLetThrough(t *testing.T) {
	r, err := configure(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if pod, waits := r.SlotWaitsForCredential(testTree, "implementer"); pod != "" || !waits {
		t.Fatalf("the slot implementer with no launcher connected = %q, %t; want none, waits", pod, waits)
	}
	if pod, waits := r.SlotWaitsForCredential(testTree, "Implementer"); waits {
		t.Fatalf("the slot Implementer, a name no claim token carries = %q, waits: a re-role into it would start with no credential wait; want a refusal", pod)
	}
}
