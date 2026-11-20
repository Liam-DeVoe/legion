package supervise

import (
	"testing"
	"time"
)

// The daemon's own controller (`controller: daemon`) is held to the registration deadline as every
// claim is. Its keeper creates the claim queued and spawns it (the daemon's controllerKeeper.keep),
// and leaves a launch that has not said it is ready to the machine: nothing of the keeper's acts on
// a registered controller. So the deadline alone ends a controller whose agent registered and never
// said it was ready, one whose host's startup never settles: at it the live process is retired, one
// launch failure is charged, and the registered session is resumed one generation later, as
// TestARegisteredAgentThatNeverSaysReadyIsRelaunchedAtTheDeadline holds for a worker. The defect
// this catches is a deadline that exempts the controller's claim: that process would stay
// registered for ever, and the startup cards' hold, which pi-envoy keeps until the host's startup
// settles, with it. The relaunched controller that says it is ready is sent the start message its
// keeper hands it at that ready (the daemon's controllerKeeper.ready).
func TestADaemonLaunchedControllerThatNeverSaysReadyIsRelaunchedAtTheDeadline(t *testing.T) {
	const start = "Legion controller start"
	h := newHarnessOf(t, controllerClaim())
	h.launch()
	h.advance(testBoot)
	h.connect()
	h.advance(testBoot)
	h.register()
	h.wantState(StateRegistered)
	alive := h.locator()

	// Just short of the registration plus the deadline, and past any deadline armed before the
	// registration: left alone.
	h.advance(deadline - time.Nanosecond)
	h.wantCalls("Suspend", 0)
	h.wantState(StateRegistered)

	h.advance(time.Nanosecond)
	if suspends := h.wantCalls("Suspend", 1); suspends[0].Locator != alive {
		t.Errorf("suspended %+v, want the live process whose controller never said ready", suspends[0].Locator)
	}
	h.wantCalls("Release", 0)
	if resumed := h.wantCalls("Resume", 1)[0]; resumed.Spec.ResumeSessionFile != sessionFile || resumed.Spec.Generation != 2 {
		t.Errorf("resumed %+v, want the registered session at generation 2", resumed.Spec)
	}
	h.wantBudgets(Budgets{LaunchFailures: 1})
	h.wantState(StateLaunching)
	h.wantPrompts(0)
	if lines := h.logs.lines("the agent registered and never said it was ready"); len(lines) != 1 {
		t.Errorf("logged %q, want one line naming the controller that never said ready", lines)
	}

	h.relaunched()
	h.must(RequestDeliver{Claim: h.token, Task: start})
	if prompts := h.wantPrompts(1); prompts[0].Message != start {
		t.Errorf("the relaunched controller was prompted %q, want the start message its keeper hands it at its ready", prompts[0].Message)
	}
	h.wantBudgets(Budgets{})
}
