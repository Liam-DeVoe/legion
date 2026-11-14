package sandbox

import (
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// fetching is a pod's status while workspace-fetch runs: Pending, workspace-init not started.
func fetching(p *corev1.Pod) {
	p.Status = corev1.PodStatus{Phase: corev1.PodPending, InitContainerStatuses: []corev1.ContainerStatus{
		{Name: fetchContainer, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		{Name: initContainer, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}},
	}}
}

// pulling is a pod's status before any init container has started: an image pull.
func pulling(p *corev1.Pod) {
	p.Status = corev1.PodStatus{Phase: corev1.PodPending, InitContainerStatuses: []corev1.ContainerStatus{
		{Name: fetchContainer, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}},
		{Name: initContainer, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}},
	}}
}

// pendingPod waits, as long as a loaded machine needs, for the pod named name that the launch
// under test makes, sets its status with status, and returns when it first saw the pod: the wait
// for the pod's launcher begins once the runtime's store holds it, so no earlier.
func (g *rig) pendingPod(name string, status func(*corev1.Pod)) time.Time {
	g.t.Helper()
	testwait.Eventually(g.t, "the new Pending pod", func() bool { return g.pod(name) != nil })
	appeared := time.Now()
	g.update(g.pod(name), status)
	return appeared
}

// The tests below time the launch's own timers (the boot timeout, the provisioning bound, the
// recheck), so each runs its rig in a synctest bubble: no real network I/O reaches the rig, the
// bubble's clock is the test's own, and what a launch does at each of those moments is read on
// it, the same at any load.

// inBubble waits, inside a synctest bubble, until holds reports true: it lets every goroutine of
// the bubble act on the moment it is at (synctest.Wait), reads, and moves the bubble's clock a
// millisecond on, so it ends at the bubble's first moment at which holds is true and costs no real
// time. The shared wait does not run in a bubble: it reads the test binary's deadline, which
// testing refuses to give a bubble's test.
func inBubble(holds func() bool) {
	for synctest.Wait(); !holds(); synctest.Wait() {
		time.Sleep(time.Millisecond)
	}
}

// pendingPodInBubble is pendingPod inside a synctest bubble.
func (g *rig) pendingPodInBubble(name string, status func(*corev1.Pod)) time.Time {
	g.t.Helper()
	inBubble(func() bool { return g.pod(name) != nil })
	appeared := time.Now()
	g.update(g.pod(name), status)
	return appeared
}

// A pod's role launchers start only once its init containers have finished, and workspace-fetch's
// clone, with its retries of a transient failure, may run past the boot timeout. A launch whose pod
// is still provisioning then keeps waiting for its launcher, and starts the role in that same pod
// once the launcher connects: the claim is charged no launch failure.
func TestALaunchWaitsForAPodProvisioningPastTheBootTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newRig(t, nil)
		g.autoStart.Store(false)
		spec := workerSpec(t)
		machine, _, _ := supervisedSandboxMachine(t, g, spec)
		launched := make(chan error, 1)
		go func() { launched <- machine.Handle(g.ctx, supervise.RequestSpawn{Claim: spec.Claim}) }()
		name := SandboxName(spec.Claim)
		g.pendingPodInBubble(name, fetching)
		uid := g.pod(name).UID

		// A second past the boot timeout on the bubble's clock, with every goroutine of the
		// launch blocked again: a launch that gave up at the boot timeout has ended by now.
		time.Sleep(g.opts.BootTimeout + time.Second)
		synctest.Wait()
		select {
		case err := <-launched:
			t.Fatalf("the launch ended while its pod was still in workspace-fetch: %v", err)
		default:
		}
		g.update(g.pod(name), func(p *corev1.Pod) { p.Status = runningStatusOf(p.Spec) })
		g.launcher(spec.Claim)
		if err := <-launched; err != nil {
			t.Fatalf("the launch failed once its pod had provisioned: %v", err)
		}
		got := machine.Claim()
		if got.Budgets.LaunchFailures != 0 || got.Locator == nil || got.Locator.Sandbox.PodUID != string(uid) {
			t.Fatalf("claim after the launch = %+v, want it launched in pod %s with no launch failure", got, uid)
		}
		if !slices.ContainsFunc(g.sent(spec.Claim), func(frame shimwire.Frame) bool {
			start, ok := frame.(shimwire.LauncherStart)
			return ok && start.Generation == got.Generation
		}) {
			t.Fatalf("the launcher was sent %v, want the generation %d start", g.sent(spec.Claim), got.Generation)
		}
	})
}

// The wait provisioning extends is still bounded once provisioning ends: the launcher then has the
// boot timeout to connect, and a launch whose launcher never does fails then, not at the end of
// ProvisionBound.
func TestALaunchGivesUpTheBootTimeoutAfterItsPodStopsProvisioning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newRig(t, nil)
		g.autoStart.Store(false)
		result := make(chan error, 1)
		go func() { _, err := g.r.Spawn(g.ctx, workerSpec(t)); result <- err }()
		name := SandboxName(workerToken)
		appeared := g.pendingPodInBubble(name, fetching)
		time.Sleep(g.opts.BootTimeout + time.Second)
		provisioned := time.Now()
		g.update(g.pod(name), func(p *corev1.Pod) { p.Status = runningStatusOf(p.Spec) })

		err := <-result
		if err == nil || !strings.Contains(err.Error(), "wait for its role launcher: timed out after") {
			t.Fatalf("Spawn = %v, want it timed out waiting for the role launcher", err)
		}
		if waited := time.Since(provisioned); waited < g.opts.BootTimeout-2*recheckInterval {
			t.Fatalf("the launch gave up %s after its pod provisioned, want about the boot timeout %s", waited, g.opts.BootTimeout)
		}
		if waited := time.Since(appeared); waited < 2*g.opts.BootTimeout {
			t.Fatalf("the launch gave up %s after its pod appeared, want the boot timeout counted from the end of provisioning", waited)
		}
	})
}

// A pod that never runs an init container, as one whose image pull never finishes, is not
// provisioning: its launch gives up at the boot timeout.
func TestALaunchWhosePodNeverProvisionsGivesUpAtTheBootTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newRig(t, nil)
		g.autoStart.Store(false)
		result := make(chan error, 1)
		go func() { _, err := g.r.Spawn(g.ctx, workerSpec(t)); result <- err }()
		name := SandboxName(workerToken)
		g.pendingPodInBubble(name, pulling)

		err := <-result
		want := "wait for its role launcher: timed out after " + g.opts.BootTimeout.String() + " waiting for role launcher " + string(workerToken)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Spawn = %v, want %q", err, want)
		}
	})
}

// The launcher's deadline is the boot timeout after the pod was last seen provisioning, and never
// past the boot timeout plus ProvisionBound from the first time it was. The last row is the edge a
// cap counted from the wait's start got wrong: a 60 s image pull, a fetch that ran the whole bound
// this case gives it, then a 90 s workspace-init, whose launcher connects 32 min 30 s after the
// wait began.
func TestTheLauncherDeadlineFollowsProvisioningUpToItsBound(t *testing.T) {
	start := time.Date(2026, 10, 13, 17, 49, 23, 0, time.UTC)
	boot, bound := 2*time.Minute, 30*time.Minute
	for _, tc := range []struct {
		name        string
		first, last time.Duration
		want        time.Duration
	}{
		{"provisioning at the start", 0, 0, boot},
		{"provisioning ten minutes in", 0, 10 * time.Minute, 10*time.Minute + boot},
		{"provisioning right up to the bound", 0, bound, bound + boot},
		{"provisioning past the bound", 0, bound + time.Minute, bound + boot},
		{"a pull, a whole fetch, then workspace-init", time.Minute, 32*time.Minute + 30*time.Second, time.Minute + bound + boot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := launcherDeadline(start.Add(tc.first), start.Add(tc.last), boot, bound).Sub(start); got != tc.want {
				t.Fatalf("launcherDeadline = start + %s, want start + %s", got, tc.want)
			}
		})
	}
}

// The launch's wait for its pod's launcher outlasts the fetch's whole clocked budget, which the
// fetch's waits and clocked stages keep to, so a fetch still within it is never cut short by the
// launch, and one that runs out of one of those bounds fails, naming why, before the launch would
// give up. The stream from the repository cache is the one part outside them, on the low-speed
// bound rather than a clock (workspace.CacheStreamLowSpeedBytes), and it runs on into the slack
// this wait holds over what the fetch actually waited. With the default 2-minute boot timeout
// and the runtime's own bound: a pod pulled in a minute whose fetch waits 35 minutes for its
// repository's cache is still waited on, and one whose fetch runs its whole FetchBudget leaves
// workspace-init the boot timeout.
func TestTheLauncherWaitOutlastsTheFetchsWholeBudget(t *testing.T) {
	g := newRig(t, nil)
	start := time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC)
	first, boot := start.Add(time.Minute), 2*time.Minute
	for _, tc := range []struct {
		name string
		last time.Duration
		want time.Duration
	}{
		{"a fetch waiting 35 minutes for its repository's cache", 35 * time.Minute, 35*time.Minute + boot},
		{"a fetch running its whole budget", workspace.FetchBudget, workspace.FetchBudget + boot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := launcherDeadline(first, first.Add(tc.last), boot, g.r.ProvisionBound()).Sub(first); got != tc.want {
				t.Fatalf("launcherDeadline = first provisioning + %s, want + %s: the fetch's own bound ends first", got, tc.want)
			}
		})
	}
}

// A launch that gives up on a pod whose workspace-fetch is still running says so, with the state
// of the repository cache that fetch fills a fresh feed from: the fetch's own failure, which would
// name it, has not come.
func TestALaunchThatGivesUpOnAFetchNamesTheRepositorysCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newRig(t, nil)
		g.autoStart.Store(false)
		g.r.provisionBound = time.Second
		spec := workerSpec(t)
		result := make(chan error, 1)
		go func() { _, err := g.r.Spawn(g.ctx, spec); result <- err }()
		appeared := g.pendingPodInBubble(SandboxName(spec.Claim), fetching)

		err := <-result
		want := "; its workspace-fetch is still running; the repository cache of " + spec.Repository.String() + ", which a fresh feed comes from alone, is "
		if err == nil || !strings.Contains(err.Error(), "wait for its role launcher: timed out after") || !strings.Contains(err.Error(), want) {
			t.Fatalf("Spawn = %v, want the launcher wait's timeout naming the running fetch and %q", err, want)
		}
		// The launcher's deadline is the boot timeout past the provisioning bound from the first
		// moment the wait saw the pod fetching, which it reads at its next recheck at the latest.
		if waited, bound := time.Since(appeared), g.r.provisionBound+g.opts.BootTimeout+recheckInterval; waited > bound {
			t.Fatalf("the launch gave up %s after its pod appeared fetching, past its bound of %s", waited, bound)
		}
	})
}

// The cap is counted from the first time the wait sees the pod provisioning, not from the wait's
// start: an image pull before workspace-fetch does not eat into the fetch's bound. With a 2 s boot
// timeout and a 3 s bound, the pod pulls for 1.5 s and provisions until 5.5 s after it appeared,
// past the 5 s a cap from the wait's start would allow; its launcher connects then, and the launch
// starts the role.
func TestALaunchCountsItsProvisioningBoundFromTheFirstInitContainer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newRig(t, nil)
		g.autoStart.Store(false)
		g.r.provisionBound = 3 * time.Second
		spec := workerSpec(t)
		result := make(chan error, 1)
		go func() { _, err := g.r.Spawn(g.ctx, spec); result <- err }()
		name := SandboxName(spec.Claim)
		appeared := g.pendingPodInBubble(name, pulling)
		time.Sleep(time.Until(appeared.Add(1500 * time.Millisecond)))
		g.update(g.pod(name), fetching)
		time.Sleep(time.Until(appeared.Add(5500 * time.Millisecond)))
		g.update(g.pod(name), func(p *corev1.Pod) { p.Status = runningStatusOf(p.Spec) })
		g.launcher(spec.Claim)

		if err := <-result; err != nil {
			t.Fatalf("Spawn = %v %s after its pod appeared, want the role started", err, time.Since(appeared).Round(time.Millisecond))
		}
	})
}
