package sandbox

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/appauth"
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

// A read that fails in a launch into a kept pod whose launcher does not wait for the credential is
// an error the launch retries. It is never a slot read as starting another App, which ends the
// launch for the pod's update round (runtime.ErrAwaitsPodUpdate). The claim `…-implementer` runs
// as the checker, a role the deployment defines as the implement App, and is launched again as the
// checker. After the setup, one read answers its first ask and fails at each later one: the
// runtime's ActsAs, or the plan reader of a role's App (appauth.RoleApps.Plan), which the launch
// asks for each role the slot may hold that is none of Legion's. The launch answers the read's
// error, sends no start, and is not runtime.ErrAwaitsPodUpdate. The control: with reads that never
// fail, the same launch starts.
func TestAFailedActsAsAskInALaunchIsRetriedNotHeldForThePodsRound(t *testing.T) {
	keyed := claim.Token("legion-legion-legion-208-implementer")
	failed := errors.New("the record could not be read")
	for _, tc := range []struct {
		name string
		// fails is the read that fails after its first ask: "actsAs", "plan", or "" for none.
		fails string
	}{
		{name: "an ActsAs that fails after its first ask", fails: "actsAs"},
		{name: "a plan read that fails after its first ask", fails: "plan"},
		{name: "control: reads that never fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var armed atomic.Bool
			asked := map[string]*atomic.Int32{"actsAs": new(atomic.Int32), "plan": new(atomic.Int32)}
			// answer is read's answer once armed: nil at its first ask, and failed at each later
			// ask where read is the one the case fails.
			answer := func(read string) error {
				if armed.Load() && asked[read].Add(1) > 1 && tc.fails == read {
					return failed
				}
				return nil
			}
			g := newRig(t, nil, withoutCredentialWait(),
				withOptions(func(o *Options) {
					o.RoleApps.Deployed = appauth.DeploymentApps{"checker": appauth.Implement}
					o.RoleApps.Plan = func(context.Context, string, claim.Role) (appauth.Planned, error) {
						return appauth.Planned{}, answer("plan")
					}
					o.ActsAs = func(_ context.Context, token claim.Token) (claim.Role, bool, error) {
						if token != keyed {
							return "", false, nil
						}
						return "", false, answer("actsAs")
					}
				}),
				withProjectTokens(refusingOwnerTokens{refuse: new(atomic.Bool)}))
			g.spawn(rootSpec(t))
			g.spawn(testSpec(t, keyed, "checker", testTree))
			sent := len(g.sent(keyed))

			again := testSpec(t, keyed, "checker", testTree)
			again.Generation++
			armed.Store(true)
			_, err := g.r.Spawn(g.ctx, again)
			starts := slices.ContainsFunc(g.sent(keyed)[sent:], func(frame shimwire.Frame) bool {
				start, ok := frame.(shimwire.LauncherStart)
				return ok && start.Generation == again.Generation
			})
			if tc.fails == "" {
				if err != nil || !starts {
					t.Fatalf("the launch with reads that never fail = %v, sending a start: %t; want it started", err, starts)
				}
				return
			}
			if n := asked[tc.fails].Load(); n < 2 {
				t.Fatalf("the launch asked the %s read %d times, want two or more: this case proves nothing", tc.fails, n)
			}
			if errors.Is(err, runtime.ErrAwaitsPodUpdate) || !errors.Is(err, failed) || starts {
				t.Fatalf("the launch with a %s read that failed = %v, sending a start: %t; want the read's error, which the launch retries, "+
					"no start, and no wait for the pod's update round", tc.fails, err, starts)
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
	if pod, waits, err := r.SlotWaitsForCredential(testTree, "implementer"); err != nil || pod != "" || !waits {
		t.Fatalf("the slot implementer with no launcher connected = %q, %t, %v; want none, waits", pod, waits, err)
	}
	if pod, waits, err := r.SlotWaitsForCredential(testTree, "Implementer"); err == nil {
		t.Fatalf("the slot Implementer, a name no claim token carries = %q, %t: a re-role into it would start with no credential wait; want a refusal", pod, waits)
	}
}
